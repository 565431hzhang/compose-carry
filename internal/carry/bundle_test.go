package carry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func bundleFixture() Bundle {
	s, _ := fixtures()
	return Bundle{Kind: BundleKind, Version: 1, Paths: s, Resources: resourceManifestFixture()}
}

func TestBundleRoundTrip(t *testing.T) {
	b := bundleFixture()
	filename := filepath.Join(t.TempDir(), "carry.json")
	if err := WriteBundle(filename, b); err != nil {
		t.Fatal(err)
	}
	data, err := ReadInput(filename)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Paths.Phase != "source" || len(decoded.Resources.Resources) != 2 {
		t.Fatal(decoded)
	}
	if err := WriteBundle(filename, b); err == nil {
		t.Fatal("overwrote bundle")
	}
	after, _ := os.ReadFile(filename)
	if string(data) != string(after) {
		t.Fatal("changed bundle")
	}
	raw, _ := json.Marshal(b)
	for _, bad := range []string{`null`, `{}`, string(raw) + `{}`,
		strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(raw), `"kind":"compose-carry.bundle"`, `"kind":"other"`, 1),
		strings.Replace(string(raw), `"version":1`, `"version":2`, 1),
		strings.Replace(string(raw), `"paths":`, `"Paths":`, 1),
		strings.Replace(string(raw), `"service":"app"`, `"service":"app","secret":"not-allowed"`, 1),
		strings.Replace(string(raw), `"resources":[`, `"Resources":[`, 1),
	} {
		if _, err := DecodeBundle([]byte(bad)); err == nil {
			t.Fatal("accepted malformed bundle", bad)
		}
	}
	for _, field := range []string{"paths", "resources"} {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		obj[field] = json.RawMessage("null")
		bad, _ := json.Marshal(obj)
		if _, err := DecodeBundle(bad); err == nil {
			t.Fatal("accepted null nested section")
		}
	}
	_, target := fixtures()
	b.Paths = target
	raw, _ = json.Marshal(b)
	if _, err := DecodeBundle(raw); err == nil {
		t.Fatal("accepted target as bundle source")
	}
}

func TestBundleTotalSize(t *testing.T) {
	b := bundleFixture()
	b.Paths.Entries[0].Dependency = dependency("app", "bind", "ref", "/"+strings.Repeat("a", MaxInputBytes/2))
	b.Resources.Resources[0].Name = strings.Repeat("b", MaxInputBytes/2)
	filename := filepath.Join(t.TempDir(), "large.json")
	if err := WriteBundle(filename, b); err == nil {
		t.Fatal("accepted oversized combined bundle")
	}
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Fatal("created oversized output")
	}
	if _, err := DecodeBundle(make([]byte, MaxInputBytes+1)); err == nil {
		t.Fatal("accepted oversized input")
	}
}

func TestCombinedReportPolicies(t *testing.T) {
	cases := []struct {
		name      string
		paths     Report
		resources ResourceReport
		code      int
	}{
		{"paths", Report{Checked: 1, Summary: Summary{Matched: 1}, Results: []Result{{Status: "matched"}}}, ResourceReport{ExitCode: 1}, 0},
		{"resources", Report{ExitCode: 1}, ResourceReport{Summary: ResourceSummary{Present: 1}, Results: []ResourceResult{{Status: "present"}}}, 0},
		{"empty", Report{ExitCode: 1}, ResourceReport{ExitCode: 1}, 1},
		{"excluded", Report{Excluded: 1, Summary: Summary{Excluded: 1}, Results: []Result{{Status: "excluded"}}}, ResourceReport{}, 1},
		{"overlap", Report{Excluded: 1, Summary: Summary{Excluded: 1}, Results: []Result{{Status: "excluded"}}}, ResourceReport{Summary: ResourceSummary{Present: 1}, Results: []ResourceResult{{Status: "present"}}}, 0},
		{"source-unverified", Report{Checked: 1, Summary: Summary{SourceUnverified: 1}, Results: []Result{{Status: "source_unverified"}}}, ResourceReport{}, 1},
		{"missing-resource", Report{Checked: 1, Summary: Summary{Matched: 1}, Results: []Result{{Status: "matched"}}}, ResourceReport{Summary: ResourceSummary{Missing: 1}, Results: []ResourceResult{{Status: "missing"}}}, 1},
		{"unverified-resource", Report{}, ResourceReport{Summary: ResourceSummary{Unverified: 1}, Results: []ResourceResult{{Status: "unverified"}}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal([]any{tc.paths, tc.resources})
			for _, all := range []bool{false, true} {
				for _, strict := range []bool{false, true} {
					r := CombineReports(tc.paths, tc.resources, all, strict)
					want := tc.code
					if strict && tc.paths.Excluded > 0 {
						want = 1
					}
					if r.ExitCode != want || r.OnlyProblems == all || r.FailOnExcluded != strict || r.Paths.Results == nil || r.Resources.Results == nil {
						t.Fatal(r)
					}
					if !reflect.DeepEqual(r.Paths.Summary, tc.paths.Summary) || r.Resources.Summary != tc.resources.Summary {
						t.Fatal("lost full summary")
					}
					if !all {
						for _, p := range r.Paths.Results {
							if p.Status == "matched" {
								t.Fatal("not filtered")
							}
						}
						for _, p := range r.Resources.Results {
							if p.Status == "present" {
								t.Fatal("not filtered")
							}
						}
					}
					raw, _ := json.Marshal(r)
					if strings.Count(string(raw), `"exit_code"`) != 1 {
						t.Fatal("ambiguous exit codes")
					}
				}
			}
			after, _ := json.Marshal([]any{tc.paths, tc.resources})
			if string(before) != string(after) {
				t.Fatal("mutated subreports")
			}
		})
	}
}

func TestBundlePrevalidation(t *testing.T) {
	b := bundleFixture()
	never := func(string) ([]byte, error) { t.Fatal("queried invalid bundle"); return nil, nil }
	if _, err := CheckBundle(b, map[string]string{"/not-known": "/new"}, nil, "default", never, false, false); err == nil {
		t.Fatal("accepted invalid paths")
	}
	if _, err := CheckBundle(b, nil, ResourceMap{"volumes": {"unknown": "new"}}, "default", never, false, false); err == nil {
		t.Fatal("accepted invalid resource map")
	}
}
