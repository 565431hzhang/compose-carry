package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
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
		if target.os == runtime.GOOS && target.arch == runtime.GOARCH {
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
		}
	}
}
