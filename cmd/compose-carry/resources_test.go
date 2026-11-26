package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

func TestDockerEnvironment(t *testing.T) {
	env := []string{"PATH=tools", "DOCKER_HOST=tcp://wrong:1", "DOCKER_CONTEXT=wrong", "docker_tls=1", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=wrong", "DOCKER_CONFIG=private-config", "SSH_AUTH_SOCK=agent"}
	want := []string{"PATH=tools", "DOCKER_CONFIG=private-config", "SSH_AUTH_SOCK=agent"}
	if got := dockerEnvironment(env); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if len(env) != 8 {
		t.Fatal("mutated input environment")
	}
}

func TestResourcesCLI(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "compose.json")
	input := `{"services":{"app":{"volumes":[{"type":"volume","source":"db"}],"networks":["edge"]}},"volumes":{"db":{"external":true,"name":"data;no-shell"}},"networks":{"edge":{"external":true}}}`
	if err := os.WriteFile(config, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		calls := 0
		runner := func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 15*time.Second {
				t.Fatal("missing query timeout")
			}
			if len(args) < 6 || args[0] != "--context" || args[1] != "selected" {
				t.Fatal(args)
			}
			if args[2] == "volume" {
				if !reflect.DeepEqual(args, []string{"--context", "selected", "volume", "ls", "--format", "{{json .Name}}"}) {
					t.Fatal(args)
				}
				return []byte("\"data;no-shell\"\n"), nil
			}
			if !reflect.DeepEqual(args, []string{"--context", "selected", "network", "ls", "--no-trunc", "--format", "[{{json .ID}},{{json .Name}}]"}) {
				t.Fatal(args)
			}
			return []byte(`["` + strings.Repeat("a", 64) + `","edge"]`), nil
		}
		var out, stderr bytes.Buffer
		args := []string{"--config", config, "--docker-context", "selected", "--format", format}
		code := runResources(args, &out, &stderr, runner)
		if code != 0 || calls != 2 {
			t.Fatal(code, calls, stderr.String())
		}
		if format == "json" {
			var report carry.ResourceReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.ExitCode != code || report.Summary.Present != 2 || report.DockerContext != "selected" {
				t.Fatal(report)
			}
		} else if !strings.Contains(out.String(), "present=2") || !strings.Contains(out.String(), "exit: 0") {
			t.Fatal(out.String())
		}
		if runResources(args, brokenWriter{}, &stderr, runner) != 2 {
			t.Fatal("ignored output failure")
		}
	}
	for _, result := range []struct {
		data   []byte
		err    error
		reason string
	}{
		{nil, errors.New("secret-error"), "query_failed"},
		{nil, context.DeadlineExceeded, "query_failed"},
		{[]byte("bad-json"), nil, "invalid_inventory"},
		{[]byte{}, nil, "not_found"},
	} {
		var out, stderr bytes.Buffer
		code := runResources([]string{"--config", config, "--docker-context", "selected", "--format", "json"}, &out, &stderr, func(context.Context, ...string) ([]byte, error) { return result.data, result.err })
		var report carry.ResourceReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if code != 1 || report.ExitCode != 1 || report.Results[0].Reason != result.reason || strings.Contains(out.String()+stderr.String(), "secret-error") {
			t.Fatal(code, report, stderr.String())
		}
	}
}

func TestResourcesArgumentsAndEmpty(t *testing.T) {
	never := func(context.Context, ...string) ([]byte, error) {
		t.Fatal("unexpected Docker invocation")
		return nil, nil
	}
	for _, args := range [][]string{nil, {"--config", "x"}, {"--docker-context", "default"}, {"--config", "x", "--docker-context", "-evil"}, {"--config", "x", "--docker-context", "bad\ncontext"}, {"--config", "x", "--docker-context", "default", "--format", "yaml"}, {"--unknown=secret"}, {"--only-problems"}} {
		var out, stderr bytes.Buffer
		if runResources(args, &out, &stderr, never) != 2 || strings.Contains(stderr.String(), "secret") {
			t.Fatal(args, stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if run([]string{"resources", "--help"}, &out, &stderr) != 0 || !strings.Contains(out.String(), "--docker-context") {
		t.Fatal(out.String())
	}
	config := filepath.Join(t.TempDir(), "compose.json")
	if err := os.WriteFile(config, []byte(`{"services":{"app":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code := runResources([]string{"--config", config, "--docker-context", "default", "--format", "json"}, &out, &stderr, never)
	var report carry.ResourceReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if code != 1 || report.Results == nil || len(report.Results) != 0 {
		t.Fatal(report)
	}
	if err := os.WriteFile(config, []byte(`{"services":{"app":{"networks":["missing"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if runResources([]string{"--config", config, "--docker-context", "default"}, &out, &stderr, never) != 2 {
		t.Fatal("accepted bad input")
	}
}

func TestBoundedCommand(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode    string
		limit   int
		timeout time.Duration
		ok      bool
	}{
		{"ok", 128, 10 * time.Second, true},
		{"fail", 128, 10 * time.Second, false},
		{"overflow", 32, 10 * time.Second, false},
		{"wait", 128, 150 * time.Millisecond, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			data, err := boundedCommand(ctx, tc.limit, executable, "-test.run=^TestResourceSubprocess$", "--", "resource-helper", tc.mode)
			if (err == nil) != tc.ok {
				t.Fatal(string(data), err)
			}
			if tc.ok && string(data) != "projected" {
				t.Fatal(string(data))
			}
			if !tc.ok && (len(data) != 0 || strings.Contains(err.Error(), "secret-diagnostic")) {
				t.Fatal(string(data), err)
			}
		})
	}
}

func TestResourceSubprocess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "resource-helper" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "ok":
		fmt.Fprint(os.Stdout, "projected")
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stdout, "partial")
		fmt.Fprint(os.Stderr, "secret-diagnostic")
		os.Exit(1)
	case "overflow":
		for {
			if _, err := io.WriteString(os.Stdout, strings.Repeat("x", 4096)); err != nil {
				os.Exit(1)
			}
		}
	case "wait":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}
