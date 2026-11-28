package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/565431hzhang/compose-carry/internal/buildinfo"
	"github.com/565431hzhang/compose-carry/internal/carry"
)

func readArchive(t *testing.T, filename string) []member {
	t.Helper()
	members := []member{}
	if strings.HasSuffix(filename, ".zip") {
		r, err := zip.OpenReader(filename)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for _, f := range r.File {
			reader, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			members = append(members, member{f.Name, data, int64(f.Mode().Perm())})
		}
	} else {
		f, err := os.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		compressed, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		for {
			header, err := reader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if header.Typeflag != tar.TypeReg {
				t.Fatal("non-regular archive member")
			}
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			members = append(members, member{header.Name, data, header.Mode})
		}
		if _, err := io.Copy(io.Discard, compressed); err != nil {
			t.Fatal("invalid gzip trailer", err)
		}
	}
	return members
}

func TestArchives(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		root := t.TempDir()
		want := []member{{"compose-carry", []byte("binary"), 0755}, {"LICENSE", []byte("license"), 0644}}
		a, b := filepath.Join(root, "a."+format), filepath.Join(root, "b."+format)
		if err := writeArchive(a, format, want); err != nil {
			t.Fatal(err)
		}
		if got := readArchive(t, a); !reflect.DeepEqual(got, want) {
			t.Fatal(got)
		}
		if err := writeArchive(b, format, want); err != nil {
			t.Fatal(err)
		}
		ab, _ := os.ReadFile(a)
		bb, _ := os.ReadFile(b)
		if !bytes.Equal(ab, bb) {
			t.Fatal("unstable archive metadata")
		}
		if err := writeArchive(a, format, want); err == nil {
			t.Fatal("overwrote archive")
		}
	}
}

func TestVersionAndEnvironment(t *testing.T) {
	for _, v := range []string{"main", "v01.2.3", "v1.2", "v1.2.3;false", "v1.2.3\n", "v1.2.3-rc1"} {
		if versionPattern.MatchString(v) {
			t.Fatal(v)
		}
		if err := run(v, filepath.Join(t.TempDir(), "output")); err == nil {
			t.Fatal("accepted invalid version")
		}
	}
	if !versionPattern.MatchString(buildinfo.Version) {
		t.Fatal("invalid source version")
	}
	env := buildEnvironment([]string{"PATH=go", "GOFLAGS=-evil", "GOOS=wrong", "GOWORK=wrong", "CGO_ENABLED=1"}, "linux", "arm64")
	if !reflect.DeepEqual(env, []string{"PATH=go", "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off", "GOAMD64=v1", "GOARM64=v8.0"}) {
		t.Fatal(env)
	}
}

func binaryMatches(data []byte, goos, arch string) bool {
	r := bytes.NewReader(data)
	switch goos {
	case "linux":
		f, err := elf.NewFile(r)
		if err != nil {
			return false
		}
		defer f.Close()
		want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
		return f.Class == elf.ELFCLASS64 && f.Machine == want[arch]
	case "windows":
		f, err := pe.NewFile(r)
		if err != nil {
			return false
		}
		defer f.Close()
		want := map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64}
		_, is64 := f.OptionalHeader.(*pe.OptionalHeader64)
		return is64 && f.Machine == want[arch]
	case "darwin":
		f, err := macho.NewFile(r)
		if err != nil {
			return false
		}
		defer f.Close()
		want := map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}
		return f.Magic == macho.Magic64 && f.Cpu == want[arch]
	}
	return false
}

func TestTargetMatrix(t *testing.T) {
	want := []struct{ os, arch, ext string }{
		{"linux", "amd64", "tar.gz"}, {"linux", "arm64", "tar.gz"},
		{"windows", "amd64", "zip"}, {"windows", "arm64", "zip"},
		{"darwin", "amd64", "tar.gz"}, {"darwin", "arm64", "tar.gz"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatal("incomplete platform matrix", targets)
	}
	for _, target := range targets {
		if binaryMatches([]byte("not executable"), target.os, target.arch) {
			t.Fatal("invalid binary accepted")
		}
	}
}

func smokeOffline(t *testing.T, executable string) {
	t.Helper()
	root := t.TempDir()
	invoke := func(want int, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(executable, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("%v exit=%d want=%d: %s", args, code, want, out)
		}
		return out
	}
	input := []byte(`{"services":{"app":{"volumes":[{"type":"bind","source":"/srv/data","target":"/data"},{"type":"volume","source":"external","target":"/external"}]}},"volumes":{"external":{"external":true,"name":"shared"}}}`)
	config := filepath.Join(root, "compose.json")
	if err := os.WriteFile(config, input, 0600); err != nil {
		t.Fatal(err)
	}
	deps, err := carry.ParseCompose(input)
	if err != nil {
		t.Fatal(err)
	}
	var dep carry.Dependency
	for _, d := range deps {
		if d.Kind == "bind" {
			dep = d
		}
	}
	o := carry.Observation{State: "observed", Type: "directory"}
	source := carry.Snapshot{Version: carry.Version, Phase: "source", Entries: []carry.Entry{{Dependency: dep, Expected: "directory", Source: o}}}
	target := carry.Snapshot{Version: carry.Version, Phase: "target", Entries: []carry.Entry{{Dependency: dep, Expected: "directory", Source: o, Target: &o}}}
	sp, tp := filepath.Join(root, "source.json"), filepath.Join(root, "target.json")
	if err := carry.WriteSnapshot(sp, source); err != nil {
		t.Fatal(err)
	}
	if err := carry.WriteSnapshot(tp, target); err != nil {
		t.Fatal(err)
	}
	out := invoke(0, "compare", "--source", sp, "--target", tp, "--format", "json")
	var report carry.Report
	if json.Unmarshal(out, &report) != nil || report.ExitCode != 0 || report.Summary.Matched != 1 {
		t.Fatal(string(out))
	}
	manifest := filepath.Join(root, "resources.json")
	invoke(0, "resource-manifest", "--config", config, "--out", manifest)
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, err := carry.DecodeResourceManifest(data)
	if err != nil || len(m.Resources) != 1 || m.Resources[0].Name != "shared" {
		t.Fatal(m, err)
	}
	if runtime.GOOS != "linux" {
		for _, args := range [][]string{
			{"source", "--config", config, "--out", "no-source.json"},
			{"target", "--manifest", sp, "--out", "no-target.json"},
			{"export", "--config", config, "--out", "no-bundle.json"},
			{"check", "--bundle", "missing.json"},
		} {
			if out := invoke(2, args...); !strings.Contains(string(out), "requires Linux") {
				t.Fatal(string(out))
			}
		}
		for _, name := range []string{"no-source.json", "no-target.json", "no-bundle.json", "carry.json"} {
			if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
				t.Fatal("unexpected scan output", name)
			}
		}
	}
	t.Logf("native %s/%s package: offline comparison, manifest export and platform guards passed", runtime.GOOS, runtime.GOARCH)
}

func TestReleaseArtifacts(t *testing.T) {
	root := os.Getenv("COMPOSE_CARRY_RELEASE_DIR")
	if root == "" {
		t.Skip("set COMPOSE_CARRY_RELEASE_DIR to validate built packages")
	}
	sums, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if len(lines) != len(targets) {
		t.Fatal("unexpected checksum count")
	}
	commit, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	nativeCount := 0
	for i, target := range targets {
		name := fmt.Sprintf("compose-carry_%s_%s_%s.%s", buildinfo.Version, target.os, target.arch, target.ext)
		filename := filepath.Join(root, name)
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if lines[i] != fmt.Sprintf("%x  %s", sha256.Sum256(data), name) {
			t.Fatal("checksum mismatch", name)
		}
		members := readArchive(t, filename)
		binary := "compose-carry"
		if target.os == "windows" {
			binary += ".exe"
		}
		expected := []string{binary, "LICENSE", "README.md", "README.zh-CN.md"}
		if len(members) != len(expected) {
			t.Fatal("unexpected package contents")
		}
		for j, m := range members {
			if m.name != expected[j] || len(m.data) == 0 {
				t.Fatal("invalid member", m.name)
			}
		}
		if members[0].mode != 0755 {
			t.Fatal("binary not executable")
		}
		if !binaryMatches(members[0].data, target.os, target.arch) {
			t.Fatal("binary header mismatch", name)
		}
		for _, other := range targets {
			if other.os == target.os && other.arch == target.arch {
				continue
			}
			if binaryMatches(members[0].data, other.os, other.arch) {
				t.Fatal("accepted wrong target header", name, other)
			}
		}
		if target.os == runtime.GOOS && target.arch == runtime.GOARCH {
			nativeCount++
			executable := filepath.Join(t.TempDir(), binary)
			if err := os.WriteFile(executable, members[0].data, 0755); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(executable, "--version").Output()
			if err != nil || string(output) != "compose-carry "+buildinfo.Version+" (commit "+commit+")\n" {
				t.Fatalf("%s %v", output, err)
			}
			output, err = exec.Command(executable, "--help").Output()
			if err != nil || !strings.Contains(string(output), buildinfo.Version) {
				t.Fatal("package help failed", err)
			}
			smokeOffline(t, executable)
		}
	}
	if nativeCount != 1 {
		t.Fatalf("expected one native package execution, got %d", nativeCount)
	}
}
