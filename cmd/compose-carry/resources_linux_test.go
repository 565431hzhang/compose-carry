package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

func TestDockerResourcesIntegration(t *testing.T) {
	if os.Getenv("COMPOSE_CARRY_TEST_RESOURCES") != "1" {
		t.Skip("set COMPOSE_CARRY_TEST_RESOURCES=1 to create and clean temporary Docker resources")
	}
	root := t.TempDir()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	prefix := "compose-carry-test-" + hex.EncodeToString(random)
	docker := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "docker", append([]string{"--context", "default"}, args...)...).CombinedOutput()
	}
	mustDocker := func(args ...string) []byte {
		t.Helper()
		out, err := docker(args...)
		if err != nil {
			t.Fatalf("Docker %v failed: %v %s", args, err, out)
		}
		return out
	}
	volume := prefix + "-data"
	network := prefix + "-edge"
	networkIDs := []string{}
	volumeCreated := false
	t.Cleanup(func() {
		for _, id := range networkIDs {
			if out, err := docker("network", "rm", id); err != nil {
				t.Errorf("cleanup pending network %s: %v %s", id, err, out)
			}
		}
		if volumeCreated {
			if out, err := docker("volume", "rm", volume); err != nil {
				t.Errorf("cleanup pending volume %s: %v %s", volume, err, out)
			}
		}
		ids, err := docker("network", "ls", "--no-trunc", "--format", "{{.ID}}")
		if err != nil {
			t.Errorf("cannot verify network cleanup: %v", err)
		} else {
			for _, remaining := range strings.Fields(string(ids)) {
				for _, id := range networkIDs {
					if remaining == id {
						t.Errorf("network remains: %s", id)
					}
				}
			}
		}
		names, err := docker("volume", "ls", "--format", "{{.Name}}")
		if err != nil {
			t.Errorf("cannot verify volume cleanup: %v", err)
		} else {
			for _, name := range strings.Fields(string(names)) {
				if name == volume {
					t.Errorf("volume remains: %s", volume)
				}
			}
		}
	})
	existing := strings.Fields(string(mustDocker("volume", "ls", "--format", "{{.Name}}")))
	for _, name := range existing {
		if name == volume {
			t.Fatal("test volume already exists")
		}
	}
	mustDocker("volume", "create", "--label", "compose-carry-test="+prefix, volume)
	volumeCreated = true
	networkID := strings.TrimSpace(string(mustDocker("network", "create", "--internal", "--label", "compose-carry-test="+prefix, network)))
	networkIDs = append(networkIDs, networkID)
	binary := filepath.Join(root, "compose-carry")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	config := filepath.Join(root, "compose.json")
	check := func(networkRef, volumeRef string, want int) carry.ResourceReport {
		t.Helper()
		compose := fmt.Sprintf("name: resource-test\nservices:\n  app:\n    image: busybox:latest\n    volumes:\n      - data:/data\n    networks:\n      - edge\nvolumes:\n  data:\n    external: true\n    name: %s\nnetworks:\n  edge:\n    external: true\n    name: %s\n", volumeRef, networkRef)
		yaml := filepath.Join(root, "compose.yaml")
		if err := os.WriteFile(yaml, []byte(compose), 0600); err != nil {
			t.Fatal(err)
		}
		rendered := mustDocker("compose", "-f", yaml, "config", "--format", "json")
		if err := os.WriteFile(config, rendered, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "resources", "--config", config, "--docker-context", "default", "--format", "json")
		// The explicit context must override unrelated inherited Docker endpoint settings.
		command.Env = append(os.Environ(), "DOCKER_HOST=tcp://127.0.0.1:1", "DOCKER_CONTEXT=nonexistent-carry-test")
		out, err := command.Output()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("exit %d want %d: %s %v", code, want, out, err)
		}
		var report carry.ResourceReport
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatal(err)
		}
		if report.ExitCode != code {
			t.Fatal(report)
		}
		return report
	}
	for _, reference := range []string{network, networkID, networkID[:12]} {
		inspected := strings.TrimSpace(string(mustDocker("network", "inspect", "--format", "{{.Id}}", reference)))
		if inspected != networkID {
			t.Fatal("Docker resolved a different network")
		}
		report := check(reference, volume, 0)
		if report.Summary != (carry.ResourceSummary{Present: 2}) {
			t.Fatal(report)
		}
	}
	report := check(prefix+"-missing-network", prefix+"-missing-volume", 1)
	if report.Summary != (carry.ResourceSummary{Missing: 2}) {
		t.Fatal(report)
	}
	if out, err := docker("network", "create", "--internal", "--label", "compose-carry-test="+prefix, network); err == nil {
		duplicate := strings.TrimSpace(string(out))
		networkIDs = append(networkIDs, duplicate)
		report = check(network, volume, 1)
		if report.Summary != (carry.ResourceSummary{Present: 1, Unverified: 1}) {
			t.Fatal(report)
		}
	} else {
		t.Log("Docker refused duplicate network names; synthetic ambiguity tests cover that case")
	}
	conflictName := networkID[:12]
	names := strings.Fields(string(mustDocker("network", "ls", "--format", "{{.Name}}")))
	for _, name := range names {
		if name == conflictName {
			t.Fatal("test conflict name already exists")
		}
	}
	conflictID := strings.TrimSpace(string(mustDocker("network", "create", "--internal", "--label", "compose-carry-test="+prefix, conflictName)))
	networkIDs = append(networkIDs, conflictID)
	report = check(conflictName, volume, 1)
	if report.Summary.Unverified != 1 {
		t.Fatal("name/ID conflict must stay unverified", report)
	}
	resolved, err := docker("network", "inspect", "--format", "{{.Id}}", conflictName)
	if err == nil && strings.TrimSpace(string(resolved)) != conflictID && strings.TrimSpace(string(resolved)) != networkID {
		t.Fatal("unexpected Docker conflict resolution")
	}
	t.Log("Docker name/ID conflict was conservatively reported unverified; no resolution priority assumed")
}
