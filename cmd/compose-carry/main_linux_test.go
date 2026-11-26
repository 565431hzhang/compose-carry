package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

func TestComposeExport(t *testing.T) {
	if os.Getenv("COMPOSE_CARRY_TEST_COMPOSE") != "1" {
		t.Skip("set COMPOSE_CARRY_TEST_COMPOSE=1 with Docker Compose installed")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config", "secret"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("private-content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	compose := `name: compose-carry-test
services:
  app:
    image: busybox:latest
    environment:
      PASSWORD: private-env-value
    volumes:
      - ./data:/data
      - named:/named
    configs:
      - settings
    secrets:
      - token
configs:
  settings:
    file: ./config
secrets:
  token:
    file: ./secret
volumes:
  named: {}
`
	input := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(input, []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "compose", "-f", input, "config", "--format", "json")
	data, err := command.Output()
	if err != nil {
		t.Fatal("docker compose config failed:", err)
	}
	deps, err := carry.ParseCompose(data)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, d := range deps {
		counts[d.Kind]++
	}
	if counts["bind"] != 1 || counts["config"] != 1 || counts["secret"] != 1 || counts["excluded"] != 1 {
		t.Fatal(counts)
	}
	source, err := carry.Source(deps)
	if err != nil || carry.SnapshotExitCode(source) != 0 {
		t.Fatal(source, err)
	}
	encoded, _ := json.Marshal(source)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("snapshot retained configuration content")
	}
}

func TestBinaryEndToEnd(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "compose-carry")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	directory := filepath.Join(root, "data")
	file := filepath.Join(root, "config")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("private-config-content"), 0600); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"services": map[string]any{"app": map[string]any{"environment": map[string]string{"PASSWORD": "private-environment-value"}, "volumes": []any{map[string]string{"type": "bind", "source": directory, "target": "/data"}}, "configs": []string{"app_config"}}}, "configs": map[string]any{"app_config": map[string]string{"file": file}}}
	data, _ := json.Marshal(config)
	input := filepath.Join(root, "compose.json")
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	command := func(want int, args ...string) []byte {
		t.Helper()
		output, err := exec.Command(binary, args...).CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("%v: exit=%d want=%d %s", args, code, want, output)
		}
		return output
	}
	source, target, fixed := filepath.Join(root, "source.json"), filepath.Join(root, "target.json"), filepath.Join(root, "fixed.json")
	command(0, "source", "--config", input, "--out", source)
	snapshot, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), "private-") {
		t.Fatal("snapshot leaked content")
	}
	command(2, "source", "--config", input, "--out", source)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory, nil, 0600); err != nil {
		t.Fatal(err)
	}
	command(1, "target", "--manifest", source, "--out", target)
	output := command(1, "compare", "--source", source, "--target", target, "--format", "json")
	var report carry.Report
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	states := map[string]bool{}
	for _, r := range report.Results {
		states[r.Status] = true
	}
	if !states["target_missing"] || !states["type_mismatch"] {
		t.Fatal(report)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("different-content-not-verified"), 0600); err != nil {
		t.Fatal(err)
	}
	command(0, "target", "--manifest", source, "--out", fixed)
	command(0, "compare", "--source", source, "--target", fixed)
	moved := filepath.Join(root, "moved-data")
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	mapping := filepath.Join(root, "paths.json")
	paths, _ := json.Marshal(map[string]string{directory: moved})
	if err := os.WriteFile(mapping, paths, 0600); err != nil {
		t.Fatal(err)
	}
	mapped := filepath.Join(root, "mapped.json")
	command(0, "target", "--manifest", source, "--path-map", mapping, "--out", mapped)
	output = command(0, "compare", "--source", source, "--target", mapped)
	if !strings.Contains(string(output), "target_path="+`"`+moved+`"`) {
		t.Fatal(string(output))
	}
	output = command(0, "compare", "--source", source, "--target", mapped, "--only-problems", "--format", "json")
	var filtered compareOutput
	if err := json.Unmarshal(output, &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.ExitCode != 0 || filtered.Results == nil || len(filtered.Results) != 0 || filtered.Summary.Matched != 2 || !filtered.OnlyProblems {
		t.Fatal(string(output))
	}
	sData, err := carry.ReadInput(source)
	if err != nil {
		t.Fatal(err)
	}
	tData, err := carry.ReadInput(mapped)
	if err != nil {
		t.Fatal(err)
	}
	s, err := carry.DecodeSnapshot(sData)
	if err != nil {
		t.Fatal(err)
	}
	targetSnapshot, err := carry.DecodeSnapshot(tData)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := carry.ParseCompose([]byte(`{"services":{"extra":{"volumes":[{"type":"volume","source":"data","target":"/data"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	o := carry.Observation{State: "excluded", Type: "unknown"}
	e := carry.Entry{Dependency: deps[0], Expected: "unknown", Source: o}
	s.Entries = append(s.Entries, e)
	p := ""
	e.Target = &o
	e.TargetPath = &p
	targetSnapshot.Entries = append(targetSnapshot.Entries, e)
	sp, tp := filepath.Join(root, "excluded-source.json"), filepath.Join(root, "excluded-target.json")
	if err := carry.WriteSnapshot(sp, s); err != nil {
		t.Fatal(err)
	}
	if err := carry.WriteSnapshot(tp, targetSnapshot); err != nil {
		t.Fatal(err)
	}
	command(0, "compare", "--source", sp, "--target", tp, "--only-problems")
	output = command(1, "compare", "--source", sp, "--target", tp, "--only-problems", "--fail-on-excluded", "--format", "json")
	if err := json.Unmarshal(output, &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.ExitCode != 1 || len(filtered.Results) != 1 || filtered.Results[0].Status != "excluded" || filtered.Summary.Matched != 2 || filtered.Summary.Excluded != 1 || !filtered.FailOnExcluded {
		t.Fatal(string(output))
	}
	for _, invalid := range []string{`{"/not-listed":"/elsewhere"}`, `{"/a":"/b","/a":"/c"}`} {
		if err := os.WriteFile(mapping, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		rejected := filepath.Join(root, "rejected.json")
		command(2, "target", "--manifest", source, "--path-map", mapping, "--out", rejected)
		if _, err := os.Stat(rejected); !os.IsNotExist(err) {
			t.Fatal("invalid mapping created output")
		}
	}
}
