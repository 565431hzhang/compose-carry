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

func ParsePathMap(data []byte) (map[string]string, error) {
	invalid := errors.New("invalid path map: expected unique canonical absolute source-to-target path pairs")
	if err := checkJSON(data); err != nil {
		return nil, invalid
	}
	obj, err := object(data)
	if err != nil {
		return nil, invalid
	}
	paths := make(map[string]string, len(obj))
	for source := range obj {
		target, err := text(obj, source, true)
		if err != nil || !validPath(source) || !validPath(target) {
			return nil, invalid
		}
		paths[source] = target
	}
	return paths, nil
}

func Target(source Snapshot) (Snapshot, error) {
	return TargetWithPaths(source, nil)
}

func TargetWithPaths(source Snapshot, paths map[string]string) (Snapshot, error) {
	if runtime.GOOS != "linux" {
		return Snapshot{}, errors.New("host scanning requires Linux; offline compare works on other platforms")
	}
	if err := ValidatePathMap(source, paths); err != nil {
		return Snapshot{}, err
	}
	target := Snapshot{Version: Version, Phase: "target", Entries: make([]Entry, len(source.Entries))}
	if paths != nil {
		target.Version = MappedVersion
	}
	for i, e := range source.Entries {
		if paths != nil {
			p := e.Dependency.Path
			if mapped, ok := paths[p]; ok {
				p = mapped
			}
			e.TargetPath = &p
		}
		o := observe(e.targetDependency())
		e.Target = &o
		target.Entries[i] = e
	}
	return target, target.Validate()
}

func ValidatePathMap(source Snapshot, paths map[string]string) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if source.Phase != "source" {
		return errors.New("target requires a source snapshot")
	}
	known := map[string]bool{}
	for _, e := range source.Entries {
		if e.Dependency.Kind != "excluded" {
			known[e.Dependency.Path] = true
		}
	}
	for from, to := range paths {
		if !known[from] || !validPath(from) || !validPath(to) {
			return errors.New("path map contains an unknown source or invalid path")
		}
	}
	return nil
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
