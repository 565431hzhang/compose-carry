package carry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtures() (Snapshot, Snapshot) {
	d := dependency("app", "bind", "volumes[0]:/data", "/srv/data")
	observed := Observation{State: "observed", Type: "directory"}
	source := Snapshot{Version: Version, Phase: "source", Entries: []Entry{{Dependency: d, Expected: "directory", Source: observed}}}
	target := Snapshot{Version: Version, Phase: "target", Entries: []Entry{{Dependency: d, Expected: "directory", Source: observed, Target: &observed}}}
	return source, target
}

func TestCompareStates(t *testing.T) {
	cases := []struct {
		source, target Observation
		kind, status   string
		code           int
	}{
		{Observation{"observed", "directory"}, Observation{"observed", "directory"}, "bind", "matched", 0},
		{Observation{"observed", "directory"}, Observation{"missing", "unknown"}, "bind", "target_missing", 1},
		{Observation{"observed", "directory"}, Observation{"observed", "file"}, "bind", "type_mismatch", 1},
		{Observation{"observed", "directory"}, Observation{"not_directory", "unknown"}, "bind", "type_mismatch", 1},
		{Observation{"observed", "file"}, Observation{"symlink", "unknown"}, "bind", "target_unverified", 1},
		{Observation{"missing", "unknown"}, Observation{"observed", "file"}, "bind", "source_unverified", 1},
		{Observation{"observed", "directory"}, Observation{"observed", "file"}, "config", "source_unverified", 1},
		{Observation{"observed", "file"}, Observation{"permission_denied", "unknown"}, "secret", "target_unverified", 1},
	}
	for _, c := range cases {
		t.Run(c.status+"/"+c.kind+"/"+c.target.State, func(t *testing.T) {
			s, d := fixtures()
			dep := dependency("app", c.kind, "reference", "/srv/data")
			s.Entries[0] = Entry{Dependency: dep, Expected: expectedType(dep, c.source), Source: c.source}
			d.Entries[0] = s.Entries[0]
			d.Entries[0].Target = &c.target
			r, err := Compare(s, d)
			if err != nil || r.ExitCode != c.code || len(r.Results) != 1 || r.Results[0].Status != c.status {
				t.Fatalf("%+v %v", r, err)
			}
			encoded, _ := json.Marshal(r.Summary)
			var counts map[string]int
			if err := json.Unmarshal(encoded, &counts); err != nil {
				t.Fatal(err)
			}
			if len(counts) != 6 || counts[c.status] != 1 {
				t.Fatal(counts)
			}
			total := 0
			for _, count := range counts {
				total += count
			}
			if total != 1 {
				t.Fatal(counts)
			}
			if SnapshotExitCode(d) != c.code {
				t.Fatal("snapshot and report disagree")
			}
		})
	}
}

func TestRejectMismatchedSnapshots(t *testing.T) {
	changes := []func(*Snapshot){
		func(s *Snapshot) { s.Version++ },
		func(s *Snapshot) { s.Phase = "source" },
		func(s *Snapshot) { s.Entries = []Entry{} },
		func(s *Snapshot) { s.Entries = append(s.Entries, s.Entries[0]) },
		func(s *Snapshot) { s.Entries[0].Dependency.Path = "/elsewhere" },
		func(s *Snapshot) { s.Entries[0].Dependency = dependency("other", "bind", "ref", "/srv/data") },
		func(s *Snapshot) { s.Entries[0].Expected = "file" },
		func(s *Snapshot) {
			s.Entries[0].Source = Observation{"observed", "file"}
			s.Entries[0].Expected = "file"
		},
		func(s *Snapshot) { s.Entries[0].Target = nil },
		func(s *Snapshot) { s.Entries[0].Target = &Observation{"matched", "file"} },
		func(s *Snapshot) { s.Entries[0].Target = &Observation{"missing", "file"} },
	}
	for i, change := range changes {
		s, d := fixtures()
		change(&d)
		if _, err := Compare(s, d); err == nil {
			t.Errorf("accepted mutation %d", i)
		}
	}
}

func TestEmptyAndExcluded(t *testing.T) {
	s := Snapshot{Version: Version, Phase: "source", Entries: []Entry{}}
	d := Snapshot{Version: Version, Phase: "target", Entries: []Entry{}}
	for i := 0; i < 2; i++ {
		r, err := Compare(s, d)
		if err != nil || r.ExitCode != 1 || r.Checked != 0 || r.Excluded != i || r.Summary != (Summary{Excluded: i}) {
			t.Fatalf("%+v %v", r, err)
		}
		o := Observation{"excluded", "unknown"}
		e := Entry{Dependency: dependency("app", "excluded", "volumes[0]:/db", ""), Expected: "unknown", Source: o}
		s.Entries = append(s.Entries, e)
		e.Target = &o
		d.Entries = append(d.Entries, e)
	}
}

func TestSnapshotDecoding(t *testing.T) {
	s, _ := fixtures()
	raw, _ := json.Marshal(s)
	decoded, err := DecodeSnapshot(raw)
	if err != nil || decoded.Entries[0] != s.Entries[0] {
		t.Fatal(err)
	}
	invalid := []string{
		`null`, `{}`, string(raw) + `{}`,
		strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(raw), `"version":1`, `"Version":1`, 1),
		strings.Replace(string(raw), `"phase":"source"`, `"phase":"source","password":"sensitive"`, 1),
		strings.Replace(string(raw), `"target":null`, `"extra":null`, 1),
		strings.Replace(string(raw), `"directory"`, `"unknown"`, 1),
	}
	for _, input := range invalid {
		if _, err := DecodeSnapshot([]byte(input)); err == nil {
			t.Errorf("accepted invalid snapshot")
		}
	}
}

func TestWriteSnapshotNoOverwrite(t *testing.T) {
	s, _ := fixtures()
	p := filepath.Join(t.TempDir(), "snapshot.json")
	if err := WriteSnapshot(p, s); err != nil {
		t.Fatal(err)
	}
	before, err := ReadInput(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(p, s); err == nil {
		t.Fatal("overwrote existing snapshot")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("changed existing file")
	}
	if _, err := DecodeSnapshot(before); err != nil {
		t.Fatal(err)
	}
}

func TestSizeLimits(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "large.json")
	f, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxInputBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInput(input); err == nil {
		t.Fatal("accepted oversized input")
	}
	if _, err := ReadInput(root); err == nil {
		t.Fatal("accepted directory input")
	}
	s, _ := fixtures()
	s.Entries[0].Dependency = dependency("app", "bind", "ref", "/"+strings.Repeat("a", MaxInputBytes))
	output := filepath.Join(root, "output.json")
	if err := WriteSnapshot(output, s); err == nil {
		t.Fatal("wrote unreadable oversized snapshot")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("created output for oversized snapshot")
	}
}

func TestPathValidation(t *testing.T) {
	for _, p := range []string{"relative", "./data", "/srv/../data", "/srv//data", "/srv/data/", "/${DATA}", "/srv/\x00bad", `C:\data`, "/srv/back\\slash"} {
		if validPath(p) {
			t.Errorf("accepted path %q", p)
		}
		d := dependency("app", "bind", "ref", p)
		if !validObservation(Observation{"invalid_path", "unknown"}, d) {
			t.Fatal("invalid path not representable")
		}
		if validObservation(Observation{"observed", "file"}, d) {
			t.Fatal("invalid path falsely observed")
		}
	}
	for _, p := range []string{"/", "/srv/data", "/srv/space here"} {
		if !validPath(p) {
			t.Errorf("rejected %s", p)
		}
	}
}

func TestPathMapParsing(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"/a":null}`, `{"/a":1}`, `{"/a":"/b","/a":"/c"}`, `{"relative":"/b"}`, `{"/a":"/b/../c"}`, `{"/a":""}`, `{"/a":"${ROOT}"}`} {
		if _, err := ParsePathMap([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	paths, err := ParsePathMap([]byte(`{"/a":"/new a"}`))
	if err != nil || paths["/a"] != "/new a" {
		t.Fatal(paths, err)
	}
	paths, err = ParsePathMap([]byte(`{}`))
	if err != nil || paths == nil {
		t.Fatal("empty explicit mapping must retain v2 intent")
	}
}

func TestMappedSnapshots(t *testing.T) {
	s, target := fixtures()
	p := "/destination/data"
	target.Version = MappedVersion
	target.Entries[0].TargetPath = &p
	raw, _ := json.Marshal(target)
	decoded, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Compare(s, decoded)
	if err != nil || r.ExitCode != 0 || r.Results[0].TargetPath != p || r.Results[0].Dependency.Path != "/srv/data" {
		t.Fatal(r, err)
	}
	for _, input := range []string{
		strings.Replace(string(raw), `,"target_path":"/destination/data"`, "", 1),
		strings.Replace(string(raw), `"target_path":"/destination/data"`, `"target_path":null`, 1),
		strings.Replace(string(raw), `"/destination/data"`, `"relative"`, 1),
		strings.Replace(string(raw), `"version":2`, `"version":1`, 1),
		strings.Replace(string(raw), `"phase":"target"`, `"phase":"source"`, 1),
	} {
		if _, err := DecodeSnapshot([]byte(input)); err == nil {
			t.Errorf("accepted invalid mapped snapshot %s", input)
		}
	}
	s.Entries[0].Source = Observation{"missing", "unknown"}
	s.Entries[0].Expected = "unknown"
	target.Entries[0].Source = s.Entries[0].Source
	target.Entries[0].Expected = s.Entries[0].Expected
	r, err = Compare(s, target)
	if err != nil || r.Results[0].Status != "source_unverified" || r.ExitCode != 1 {
		t.Fatal(r, err)
	}
	target.Entries[0].Source = Observation{"permission_denied", "unknown"}
	if _, err := Compare(s, target); err == nil {
		t.Fatal("accepted altered source evidence")
	}
}

func FuzzParseCompose(f *testing.F) {
	f.Add([]byte(`{"services":{"app":{"volumes":[{"type":"bind","source":"/srv/data","target":"/data"}]}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		deps, err := ParseCompose(data)
		if err == nil {
			for _, d := range deps {
				if d != dependency(d.Service, d.Kind, d.Reference, d.Path) {
					t.Fatal("invalid identity")
				}
			}
		}
	})
}
