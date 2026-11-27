package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

func TestManifestCLI(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "compose.json")
	manifest := filepath.Join(root, "resources.json")
	input := `{"services":{"app":{"environment":{"PASSWORD":"private-value"},"volumes":[{"type":"volume","source":"data"}]}},"volumes":{"data":{"external":true,"name":"old"},"unused":{"external":true,"name":"not-exported"}}}`
	if err := os.WriteFile(config, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	var out, stderr bytes.Buffer
	args := []string{"resource-manifest", "--config", config, "--out", manifest}
	if code := run(args, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-value") || strings.Contains(string(raw), "not-exported") || !strings.Contains(out.String(), "export is not verification") {
		t.Fatal(string(raw), out.String())
	}
	if code := run(args, &out, &stderr); code != 2 {
		t.Fatal("overwrote output")
	}
	after, _ := os.ReadFile(manifest)
	if !bytes.Equal(raw, after) {
		t.Fatal("changed existing output")
	}
	mapping := filepath.Join(root, "map.json")
	if err := os.WriteFile(mapping, []byte(`{"volumes":{"old":"renamed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "text"} {
		out.Reset()
		stderr.Reset()
		calls := 0
		code := runResources([]string{"--manifest", manifest, "--resource-map", mapping, "--docker-context", "target", "--format", format}, &out, &stderr, func(context.Context, ...string) ([]byte, error) { calls++; return []byte(`"renamed"`), nil })
		if code != 0 || calls != 1 {
			t.Fatal(code, calls, stderr.String())
		}
		if format == "json" {
			var r carry.ResourceReport
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if r.ExitCode != 0 || r.Results[0].Name != "old" || r.Results[0].CheckedName != "renamed" || r.Results[0].References[0].Key != "data" {
				t.Fatal(r)
			}
		} else if !strings.Contains(out.String(), `name="old" checked_name="renamed"`) {
			t.Fatal(out.String())
		}
	}
	never := func(context.Context, ...string) ([]byte, error) {
		t.Fatal("invalid input invoked Docker")
		return nil, nil
	}
	if code := runResources([]string{"--config", config, "--manifest", manifest, "--docker-context", "target"}, &out, &stderr, never); code != 2 {
		t.Fatal(code)
	}
	if err := os.WriteFile(mapping, []byte(`{"volumes":{"unknown":"renamed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runResources([]string{"--manifest", manifest, "--resource-map", mapping, "--docker-context", "target"}, &out, &stderr, never); code != 2 {
		t.Fatal(code)
	}
	if err := os.WriteFile(manifest, []byte(`{"kind":"path-snapshot","version":1,"resources":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runResources([]string{"--manifest", manifest, "--docker-context", "target"}, &out, &stderr, never); code != 2 {
		t.Fatal(code)
	}
}

func TestEmptyManifestCLI(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "compose.json")
	manifest := filepath.Join(root, "empty.json")
	if err := os.WriteFile(config, []byte(`{"services":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if run([]string{"resource-manifest", "--config", config, "--out", manifest}, &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(out.String(), "Exported 0") {
		t.Fatal(out.String())
	}
	out.Reset()
	if runResources([]string{"--manifest", manifest, "--docker-context", "default", "--format", "json"}, &out, &stderr, func(context.Context, ...string) ([]byte, error) {
		t.Fatal("empty manifest queried Docker")
		return nil, nil
	}) != 1 {
		t.Fatal(stderr.String())
	}
	var report carry.ResourceReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Results == nil || len(report.Results) != 0 {
		t.Fatal(report, err)
	}
	for _, args := range [][]string{{"resource-manifest"}, {"resource-manifest", "--unknown=private-value"}, {"resource-manifest", "--config", config, "--out", filepath.Join(root, "bad"), "extra"}} {
		stderr.Reset()
		if run(args, &out, &stderr) != 2 || strings.Contains(stderr.String(), "private-value") {
			t.Fatal(args, stderr.String())
		}
	}
	if run([]string{"resource-manifest", "--help"}, &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	if run([]string{"resource-manifest", "--config", config, "--out", filepath.Join(root, "written.json")}, brokenWriter{}, &stderr) != 2 {
		t.Fatal("ignored write failure")
	}
}
