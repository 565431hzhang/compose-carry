package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

func TestBundleLinuxWorkflow(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "settings")
	if err := os.WriteFile(file, []byte("private-file-content"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "compose.json")
	bundle := filepath.Join(root, "carry.json")
	data := []byte(fmt.Sprintf(`{"services":{"app":{"environment":{"PASSWORD":"private-password"},"configs":["settings"],"volumes":[{"type":"volume","source":"data","target":"/data"}]}},"configs":{"settings":{"file":%q}},"volumes":{"data":{"external":true,"name":"old"}}}`, file))
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	never := func(context.Context, ...string) ([]byte, error) { t.Fatal("unexpected Docker call"); return nil, nil }
	var out, stderr bytes.Buffer
	if runBundle("export", []string{"--config", config, "--out", bundle}, &out, &stderr, never) != 0 {
		t.Fatal(stderr.String())
	}
	original, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(original), "private-") || !strings.Contains(out.String(), "Export is not verification") {
		t.Fatal(out.String())
	}
	info, err := os.Stat(bundle)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if runBundle("export", []string{"--config", config, "--out", bundle}, &out, &stderr, never) != 2 {
		t.Fatal("overwritten")
	}
	out.Reset()
	if runBundle("check", []string{"--bundle", bundle, "--format", "json"}, &out, &stderr, never) != 1 {
		t.Fatal(stderr.String())
	}
	var report carry.BundleReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Paths.Summary.Matched != 1 || report.Paths.Excluded != 1 || report.Resources.Summary.Unverified != 1 || report.Resources.Results[0].Reason != "context_required" {
		t.Fatal(report)
	}
	moved := filepath.Join(root, "moved")
	if err := os.Rename(file, moved); err != nil {
		t.Fatal(err)
	}
	paths := filepath.Join(root, "paths.json")
	raw, _ := json.Marshal(map[string]string{file: moved})
	if err := os.WriteFile(paths, raw, 0600); err != nil {
		t.Fatal(err)
	}
	names := filepath.Join(root, "names.json")
	if err := os.WriteFile(names, []byte(`{"volumes":{"old":"new"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			for _, format := range []string{"json", "text"} {
				args := []string{"--bundle", bundle, "--path-map", paths, "--resource-map", names, "--docker-context", "selected", "--format", format, fmt.Sprintf("--all=%t", all), fmt.Sprintf("--fail-on-excluded=%t", strict)}
				calls := 0
				runner := func(_ context.Context, args ...string) ([]byte, error) {
					calls++
					if args[1] != "selected" || args[2] != "volume" {
						t.Fatal(args)
					}
					return []byte(`"new"`), nil
				}
				out.Reset()
				stderr.Reset()
				code := runBundle("check", args, &out, &stderr, runner)
				want := 0
				if strict {
					want = 1
				}
				if code != want || calls != 1 {
					t.Fatal(code, calls, stderr.String())
				}
				if format == "json" {
					if err := json.Unmarshal(out.Bytes(), &report); err != nil {
						t.Fatal(err)
					}
					if report.ExitCode != code || report.Paths.Summary.Matched != 1 || report.Resources.Summary.Present != 1 {
						t.Fatal(report)
					}
					if all {
						if len(report.Paths.Results) != 2 || report.Resources.Results[0].CheckedName != "new" {
							t.Fatal(report)
						}
					} else {
						if len(report.Paths.Results) != 1 || len(report.Resources.Results) != 0 {
							t.Fatal(report)
						}
					}
				} else {
					if !strings.Contains(out.String(), "matched=1") || !strings.Contains(out.String(), "present=1") {
						t.Fatal(out.String())
					}
				}
				if runBundle("check", args, brokenWriter{}, &stderr, runner) != 2 {
					t.Fatal("ignored report write failure")
				}
			}
		}
	}
	for _, tc := range []struct {
		data   string
		err    error
		reason string
	}{
		{"", nil, "not_found"},
		{"private-partial", fmt.Errorf("private-error: %w", carry.QueryTimeout), "timeout"},
		{`"new"` + "\n" + `"new"`, nil, "ambiguous_reference"},
	} {
		out.Reset()
		stderr.Reset()
		code := runBundle("check", []string{"--bundle", bundle, "--path-map", paths, "--resource-map", names, "--docker-context", "selected", "--format", "json"}, &out, &stderr, func(context.Context, ...string) ([]byte, error) { return []byte(tc.data), tc.err })
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if code != 1 || report.ExitCode != code || report.Paths.Summary.Matched != 1 || report.Resources.Results[0].Reason != tc.reason || strings.Contains(out.String()+stderr.String(), "private-") {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	for _, invalid := range [][]string{{"--path-map", names}, {"--resource-map", paths}, {"--docker-context", ""}, {"--format", "xml"}} {
		args := append([]string{"--bundle", bundle}, invalid...)
		if runBundle("check", args, &out, &stderr, never) != 2 {
			t.Fatal("accepted invalid input", args)
		}
	}
	after, _ := os.ReadFile(bundle)
	if !bytes.Equal(original, after) {
		t.Fatal("changed source bundle")
	}
	if _, err := os.Stat(filepath.Join(root, "target.json")); !os.IsNotExist(err) {
		t.Fatal("check wrote target snapshot")
	}
}

func TestBundleLinuxCases(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, input string
		sourceBad   bool
		code        int
		queries     int
	}{
		{"empty", `{"services":{}}`, false, 1, 0},
		{"excluded", `{"services":{"app":{"volumes":[{"type":"volume","source":"managed","target":"/data"}]}},"volumes":{"managed":{}}}`, false, 1, 0},
		{"resource-only", `{"services":{"app":{"networks":["edge"]}},"networks":{"edge":{"external":true}}}`, false, 0, 1},
		{"source-missing", fmt.Sprintf(`{"services":{"app":{"configs":["c"]}},"configs":{"c":{"file":%q}}}`, filepath.Join(root, "absent")), true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := filepath.Join(root, tc.name+".json")
			bundle := filepath.Join(root, tc.name+"-bundle.json")
			if err := os.WriteFile(config, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			never := func(context.Context, ...string) ([]byte, error) { t.Fatal("export queried Docker"); return nil, nil }
			if runBundle("export", []string{"--config", config, "--out", bundle}, &out, &stderr, never) != 0 {
				t.Fatal(stderr.String())
			}
			if tc.sourceBad && !strings.Contains(out.String(), "source_unverified=1") {
				t.Fatal(out.String())
			}
			calls := 0
			runner := func(context.Context, ...string) ([]byte, error) {
				calls++
				return []byte(`["` + strings.Repeat("a", 64) + `","edge"]`), nil
			}
			out.Reset()
			if code := runBundle("check", []string{"--bundle", bundle, "--docker-context", "default", "--format", "json"}, &out, &stderr, runner); code != tc.code || calls != tc.queries {
				t.Fatal(code, calls, stderr.String())
			}
			if tc.sourceBad && !strings.Contains(out.String(), `"source_unverified": 1`) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestBundleBinaryDefaults(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "compose-carry")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	file := filepath.Join(root, "config")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"services":{"app":{"configs":["c"]}},"configs":{"c":{"file":%q}}}`, file)
	if err := os.WriteFile(filepath.Join(root, "compose.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"export", "--config", "compose.json"}, {"check", "--format", "json"}} {
		command := exec.Command(binary, args...)
		command.Dir = root
		command.Env = []string{"PATH=", "HOME=" + root}
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		if args[0] == "check" {
			var r carry.BundleReport
			if json.Unmarshal(out, &r) != nil || r.ExitCode != 0 || r.Paths.Summary.Matched != 1 || len(r.Paths.Results) != 0 {
				t.Fatal(string(out))
			}
		}
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 4 {
		t.Fatal("unexpected files", entries)
	}
}
