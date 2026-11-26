package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }

func TestCompareOptions(t *testing.T) {
	cases := []struct {
		name   string
		states []string
	}{
		{"mixed", []string{"matched", "target_missing", "type_mismatch", "source_unverified", "target_unverified", "excluded"}},
		{"matched", []string{"matched", "matched"}},
		{"matched-excluded", []string{"matched", "excluded"}},
		{"excluded", []string{"excluded"}},
		{"empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := carry.Snapshot{Version: carry.Version, Phase: "source", Entries: []carry.Entry{}}
			target := carry.Snapshot{Version: carry.MappedVersion, Phase: "target", Entries: []carry.Entry{}}
			counts := map[string]int{"matched": 0, "target_missing": 0, "type_mismatch": 0, "source_unverified": 0, "target_unverified": 0, "excluded": 0}
			for i, state := range tc.states {
				counts[state]++
				kind := "bind"
				if state == "excluded" {
					kind = "volume"
				}
				deps, err := carry.ParseCompose([]byte(fmt.Sprintf(`{"services":{"app":{"volumes":[{"type":%q,"source":"/srv/data","target":%q}]}}}`, kind, fmt.Sprintf("/mount%d", i))))
				if err != nil {
					t.Fatal(err)
				}
				observed := carry.Observation{State: "observed", Type: "directory"}
				e := carry.Entry{Dependency: deps[0], Expected: "directory", Source: observed}
				to := observed
				p := "/destination/data"
				switch state {
				case "target_missing":
					to = carry.Observation{State: "missing", Type: "unknown"}
				case "type_mismatch":
					to.Type = "file"
				case "source_unverified":
					e.Source = carry.Observation{State: "missing", Type: "unknown"}
					e.Expected = "unknown"
				case "target_unverified":
					to = carry.Observation{State: "symlink", Type: "unknown"}
				case "excluded":
					e.Source = carry.Observation{State: "excluded", Type: "unknown"}
					e.Expected = "unknown"
					to = e.Source
					p = ""
				}
				source.Entries = append(source.Entries, e)
				e.Target = &to
				e.TargetPath = &p
				target.Entries = append(target.Entries, e)
			}
			root := t.TempDir()
			sp, tp := filepath.Join(root, "source.json"), filepath.Join(root, "target.json")
			if err := carry.WriteSnapshot(sp, source); err != nil {
				t.Fatal(err)
			}
			if err := carry.WriteSnapshot(tp, target); err != nil {
				t.Fatal(err)
			}
			beforeS, _ := os.ReadFile(sp)
			beforeT, _ := os.ReadFile(tp)
			checked := len(tc.states) - counts["excluded"]
			for _, only := range []bool{false, true} {
				for _, strict := range []bool{false, true} {
					for _, format := range []string{"text", "json"} {
						args := []string{"compare", "--source", sp, "--target", tp, "--format", format, fmt.Sprintf("--only-problems=%t", only), fmt.Sprintf("--fail-on-excluded=%t", strict)}
						wantCode := 0
						if checked == 0 || checked != counts["matched"] || (strict && counts["excluded"] > 0) {
							wantCode = 1
						}
						shown := len(tc.states)
						if only {
							shown -= counts["matched"]
						}
						var out, stderr bytes.Buffer
						code := run(args, &out, &stderr)
						if code != wantCode {
							t.Fatalf("%v exit %d: %s", args, code, stderr.String())
						}
						if format == "json" {
							var got compareOutput
							if err := json.Unmarshal(out.Bytes(), &got); err != nil {
								t.Fatal(err)
							}
							if got.ExitCode != code || got.Checked != checked || got.Excluded != counts["excluded"] || got.Results == nil || len(got.Results) != shown || got.OnlyProblems != only || got.FailOnExcluded != strict {
								t.Fatal(out.String())
							}
							encoded, _ := json.Marshal(got.Summary)
							var summary map[string]int
							json.Unmarshal(encoded, &summary)
							if len(summary) != 6 {
								t.Fatal(summary)
							}
							for status, count := range counts {
								if summary[status] != count {
									t.Fatal(summary, counts)
								}
							}
							for _, r := range got.Results {
								if only && r.Status == "matched" {
									t.Fatal("matched result not hidden")
								}
								if r.Status != "excluded" && (r.TargetPath != "/destination/data" || r.Dependency.Path != "/srv/data") {
									t.Fatal("lost mapping")
								}
							}
						} else {
							for status, count := range counts {
								if !strings.Contains(out.String(), fmt.Sprintf("%s=%d", status, count)) {
									t.Fatal(out.String())
								}
							}
							if strings.Count(out.String(), "service=") != shown || !strings.Contains(out.String(), fmt.Sprintf("exit: %d", code)) {
								t.Fatal(out.String())
							}
							if only && checked > 0 && shown == 0 && !strings.Contains(out.String(), "No in-scope problems") {
								t.Fatal(out.String())
							}
						}
						if run(args, brokenWriter{}, &stderr) != 2 {
							t.Fatal("output failure must return 2")
						}
					}
				}
			}
			afterS, _ := os.ReadFile(sp)
			afterT, _ := os.ReadFile(tp)
			if !bytes.Equal(beforeS, afterS) || !bytes.Equal(beforeT, afterT) {
				t.Fatal("modified snapshots")
			}
			var out, stderr bytes.Buffer
			if code := run([]string{"compare", "--source", sp, "--target", sp, "--only-problems", "--fail-on-excluded"}, &out, &stderr); code != 2 {
				t.Fatal("options bypassed phase validation")
			}
		})
	}
	for _, command := range []string{"source", "target"} {
		for _, option := range []string{"--only-problems", "--fail-on-excluded"} {
			var out, stderr bytes.Buffer
			if run([]string{command, option}, &out, &stderr) != 2 || !strings.Contains(stderr.String(), "invalid arguments") {
				t.Fatal(command, option, stderr.String())
			}
		}
	}
}

func TestPrepareReportDoesNotMutate(t *testing.T) {
	r := carry.Report{Results: []carry.Result{{Status: "matched"}, {Status: "excluded"}}, Excluded: 1, Checked: 1, Summary: carry.Summary{Matched: 1, Excluded: 1}}
	got := prepareReport(r, true, true)
	if r.ExitCode != 0 || len(r.Results) != 2 || r.Results[0].Status != "matched" || got.ExitCode != 1 || len(got.Results) != 1 || got.Summary != r.Summary {
		t.Fatal(r, got)
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
