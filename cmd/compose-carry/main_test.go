package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/565431hzhang/compose-carry/internal/carry"
)

func TestArguments(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, 2}, {[]string{"--help"}, 0}, {[]string{"--version"}, 0}, {[]string{"bad"}, 2},
		{[]string{"source", "--help"}, 0}, {[]string{"source"}, 2}, {[]string{"source", "--unknown=private-value"}, 2},
		{[]string{"target"}, 2}, {[]string{"compare"}, 2}, {[]string{"compare", "--format", "xml"}, 2},
		{[]string{"compare", "unexpected"}, 2},
	}
	for _, c := range cases {
		var out, err bytes.Buffer
		if code := run(c.args, &out, &err); code != c.code {
			t.Fatalf("%v: %d", c.args, code)
		}
		if strings.Contains(err.String(), "private-value") {
			t.Fatal("echoed invalid input")
		}
	}
}

func TestOfflineCompareCLI(t *testing.T) {
	root := t.TempDir()
	deps, err := carry.ParseCompose([]byte(`{"services":{"app":{"volumes":[{"type":"bind","source":"/srv/data","target":"/data"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	o := carry.Observation{State: "observed", Type: "directory"}
	s := carry.Snapshot{Version: carry.Version, Phase: "source", Entries: []carry.Entry{{Dependency: deps[0], Expected: "directory", Source: o}}}
	target := carry.Snapshot{Version: carry.Version, Phase: "target", Entries: []carry.Entry{{Dependency: deps[0], Expected: "directory", Source: o, Target: &o}}}
	sp, tp := filepath.Join(root, "source.json"), filepath.Join(root, "target.json")
	if err := carry.WriteSnapshot(sp, s); err != nil {
		t.Fatal(err)
	}
	if err := carry.WriteSnapshot(tp, target); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		var out, stderr bytes.Buffer
		if code := run([]string{"compare", "--source", sp, "--target", tp, "--format", format}, &out, &stderr); code != 0 {
			t.Fatal(code, stderr.String())
		}
		if format == "json" {
			var r carry.Report
			if json.Unmarshal(out.Bytes(), &r) != nil || r.ExitCode != 0 || r.Checked != 1 {
				t.Fatal(out.String())
			}
		} else if !strings.Contains(out.String(), "matched") {
			t.Fatal(out.String())
		}
	}
}

func TestRejectScanOutsideLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("platform rejection")
	}
	root := t.TempDir()
	input := filepath.Join(root, "compose.json")
	output := filepath.Join(root, "source.json")
	if err := os.WriteFile(input, []byte(`{"services":{"app":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"source", "--config", input, "--out", output}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "requires Linux") {
		t.Fatal(code, stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("created unsupported snapshot")
	}
}
