package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/565431hzhang/compose-carry/internal/buildinfo"
)

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
var archiveTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
var targets = []struct{ os, arch, ext string }{
	{"linux", "amd64", "tar.gz"}, {"linux", "arm64", "tar.gz"},
	{"windows", "amd64", "zip"}, {"windows", "arm64", "zip"},
	{"darwin", "amd64", "tar.gz"}, {"darwin", "arm64", "tar.gz"},
}

type member struct {
	name string
	data []byte
	mode int64
}

func main() {
	version := flag.String("version", buildinfo.Version, "must match the source version")
	out := flag.String("out", "dist", "new output directory (must not exist)")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	if err := run(*version, *out); err != nil {
		fmt.Fprintln(os.Stderr, "package-release:", err)
		os.Exit(1)
	}
}

func gitOutput(args ...string) (string, error) {
	data, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed", strings.Join(args, " "))
	}
	return strings.TrimSpace(string(data)), nil
}

func buildEnvironment(env []string, goos, arch string) []string {
	result := []string{}
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOWORK", "GOAMD64", "GOARM64":
			continue
		}
		result = append(result, item)
	}
	return append(result, "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off", "GOAMD64=v1", "GOARM64=v8.0")
}

func run(version, out string) error {
	if !versionPattern.MatchString(version) || version != buildinfo.Version {
		return errors.New("version must be vMAJOR.MINOR.PATCH and match internal/buildinfo/version.go")
	}
	if out == "" {
		return errors.New("output directory is required")
	}
	root, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return err
	}
	cwdInfo, err := os.Stat(cwd)
	if err != nil {
		return err
	}
	if !os.SameFile(rootInfo, cwdInfo) {
		return errors.New("run from the repository root")
	}
	status, err := gitOutput("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("release packaging requires a clean working tree; commit changes first")
	}
	commit, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("invalid commit ID")
	}
	docs := []member{}
	for _, name := range []string{"LICENSE", "README.md", "README.zh-CN.md"} {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		docs = append(docs, member{name, data, 0644})
	}
	if err := os.Mkdir(out, 0755); err != nil {
		return fmt.Errorf("create new output directory: %w", err)
	}
	temp, err := os.MkdirTemp(out, ".build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	sums := ""
	for _, target := range targets {
		binaryName := "compose-carry"
		if target.os == "windows" {
			binaryName += ".exe"
		}
		binary := filepath.Join(temp, binaryName)
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/565431hzhang/compose-carry/internal/buildinfo.Commit="+commit, "-o", binary, "./cmd/compose-carry")
		command.Env = buildEnvironment(os.Environ(), target.os, target.arch)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("build %s/%s: %w", target.os, target.arch, err)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		members := append([]member{{binaryName, data, 0755}}, docs...)
		name := fmt.Sprintf("compose-carry_%s_%s_%s.%s", version, target.os, target.arch, target.ext)
		filename := filepath.Join(out, name)
		if err := writeArchive(filename, target.ext, members); err != nil {
			return err
		}
		f, err := os.Open(filename)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		sums += fmt.Sprintf("%x  %s\n", hash.Sum(nil), name)
		fmt.Println("Built", name)
	}
	f, err := os.OpenFile(filepath.Join(out, "SHA256SUMS"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(sums)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func writeArchive(filename, format string, members []member) error {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if format == "zip" {
		writer := zip.NewWriter(f)
		for _, item := range members {
			header := &zip.FileHeader{Name: item.name, Method: zip.Deflate, Modified: archiveTime}
			header.SetMode(os.FileMode(item.mode))
			w, e := writer.CreateHeader(header)
			if e == nil {
				_, e = w.Write(item.data)
			}
			if e != nil {
				writer.Close()
				f.Close()
				return e
			}
		}
		err = writer.Close()
	} else if format == "tar.gz" {
		compressed := gzip.NewWriter(f)
		writer := tar.NewWriter(compressed)
		for _, item := range members {
			e := writer.WriteHeader(&tar.Header{Name: item.name, Mode: item.mode, Size: int64(len(item.data)), ModTime: archiveTime, Typeflag: tar.TypeReg})
			if e == nil {
				_, e = writer.Write(item.data)
			}
			if e != nil {
				writer.Close()
				compressed.Close()
				f.Close()
				return e
			}
		}
		err = writer.Close()
		gzipErr := compressed.Close()
		if err == nil {
			err = gzipErr
		}
	} else {
		err = errors.New("unsupported archive format")
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
