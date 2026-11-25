package carry

import (
	"errors"
	"path"
	"runtime"
	"strings"
)

func validPath(p string) bool {
	return strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsAny(p, "\x00$\\")
}

func observe(d Dependency) Observation {
	if d.Kind == "excluded" {
		return Observation{State: "excluded", Type: "unknown"}
	}
	if !validPath(d.Path) {
		return Observation{State: "invalid_path", Type: "unknown"}
	}
	return inspectPath(d.Path)
}

func Source(deps []Dependency) (Snapshot, error) {
	if runtime.GOOS != "linux" {
		return Snapshot{}, errors.New("host scanning requires Linux; offline compare works on other platforms")
	}
	s := Snapshot{Version: Version, Phase: "source", Entries: []Entry{}}
	for _, d := range deps {
		o := observe(d)
		s.Entries = append(s.Entries, Entry{Dependency: d, Expected: expectedType(d, o), Source: o})
	}
	return s, s.Validate()
}

func Target(source Snapshot) (Snapshot, error) {
	if runtime.GOOS != "linux" {
		return Snapshot{}, errors.New("host scanning requires Linux; offline compare works on other platforms")
	}
	if err := source.Validate(); err != nil {
		return Snapshot{}, err
	}
	if source.Phase != "source" {
		return Snapshot{}, errors.New("target requires a source snapshot")
	}
	target := Snapshot{Version: Version, Phase: "target", Entries: make([]Entry, len(source.Entries))}
	for i, e := range source.Entries {
		o := observe(e.Dependency)
		e.Target = &o
		target.Entries[i] = e
	}
	return target, target.Validate()
}

func SnapshotExitCode(s Snapshot) int {
	checked := 0
	for _, e := range s.Entries {
		if e.Dependency.Kind == "excluded" {
			continue
		}
		checked++
		if e.Source.State != "observed" || e.Source.Type != e.Expected {
			return 1
		}
		if e.Target != nil && (e.Target.State != "observed" || e.Target.Type != e.Expected) {
			return 1
		}
	}
	if checked == 0 {
		return 1
	}
	return 0
}
