package carry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
)

const Version = 1
const MappedVersion = 2
const MaxInputBytes = 16 << 20

type Observation struct {
	State string `json:"state"`
	Type  string `json:"type"`
}

type Entry struct {
	Dependency Dependency   `json:"dependency"`
	Expected   string       `json:"expected"`
	Source     Observation  `json:"source"`
	Target     *Observation `json:"target"`
	TargetPath *string      `json:"target_path,omitempty"`
}

func (e Entry) targetDependency() Dependency {
	d := e.Dependency
	if e.TargetPath != nil {
		d.Path = *e.TargetPath
	}
	return d
}

type Snapshot struct {
	Version int     `json:"version"`
	Phase   string  `json:"phase"`
	Entries []Entry `json:"entries"`
}

func expectedType(d Dependency, o Observation) string {
	if d.Kind == "config" || d.Kind == "secret" {
		return "file"
	}
	if d.Kind == "bind" && o.State == "observed" {
		return o.Type
	}
	return "unknown"
}

func validObservation(o Observation, d Dependency) bool {
	if d.Kind == "excluded" {
		return o == (Observation{State: "excluded", Type: "unknown"})
	}
	if !validPath(d.Path) {
		return o == (Observation{State: "invalid_path", Type: "unknown"})
	}
	switch o.State {
	case "observed":
		return o.Type == "file" || o.Type == "directory"
	case "missing", "permission_denied", "symlink", "special", "not_directory", "io_error":
		return o.Type == "unknown"
	}
	return false
}

func (s Snapshot) Validate() error {
	invalid := errors.New("invalid snapshot structure or state")
	if (s.Version != Version && s.Version != MappedVersion) || (s.Phase != "source" && s.Phase != "target") || s.Entries == nil {
		return invalid
	}
	if s.Version == MappedVersion && s.Phase != "target" {
		return invalid
	}
	seen := map[string]bool{}
	for _, e := range s.Entries {
		d := e.Dependency
		if d.Service == "" || d.Reference == "" || seen[d.ID] || d != dependency(d.Service, d.Kind, d.Reference, d.Path) {
			return invalid
		}
		seen[d.ID] = true
		switch d.Kind {
		case "bind", "config", "secret":
			if d.Path == "" {
				return invalid
			}
		case "excluded":
			if d.Path != "" {
				return invalid
			}
		default:
			return invalid
		}
		if !validObservation(e.Source, d) || e.Expected != expectedType(d, e.Source) {
			return invalid
		}
		if s.Version == Version && e.TargetPath != nil {
			return invalid
		}
		if s.Version == MappedVersion {
			if e.TargetPath == nil {
				return invalid
			}
			if d.Kind == "excluded" {
				if *e.TargetPath != "" {
					return invalid
				}
			} else if *e.TargetPath != d.Path && (!validPath(d.Path) || !validPath(*e.TargetPath)) {
				return invalid
			}
		}
		if s.Phase == "source" && e.Target != nil {
			return invalid
		}
		if s.Phase == "target" && (e.Target == nil || !validObservation(*e.Target, e.targetDependency())) {
			return invalid
		}
	}
	return nil
}

func ReadInput(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("input must be an accessible regular file")
	}
	f, err := openInput(path)
	if err != nil {
		return nil, errors.New("cannot open input file")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("input must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxInputBytes+1))
	if err != nil {
		return nil, errors.New("cannot read input file")
	}
	if len(data) > MaxInputBytes {
		return nil, errors.New("input exceeds 16 MiB limit")
	}
	return data, nil
}

func DecodeSnapshot(data []byte) (Snapshot, error) {
	var s Snapshot
	if checkJSON(data) != nil || !snapshotKeys(data) {
		return s, errors.New("invalid snapshot JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, errors.New("invalid snapshot JSON")
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].Dependency.ID < s.Entries[j].Dependency.ID })
	return s, nil
}

func exactKeys(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
	obj, err := object(raw)
	if err != nil || len(obj) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if _, ok := obj[key]; !ok {
			return nil, false
		}
	}
	return obj, true
}

func snapshotKeys(data []byte) bool {
	root, ok := exactKeys(data, "version", "phase", "entries")
	if !ok {
		return false
	}
	var entries []json.RawMessage
	if json.Unmarshal(root["entries"], &entries) != nil || entries == nil {
		return false
	}
	var version int
	if json.Unmarshal(root["version"], &version) != nil || (version != Version && version != MappedVersion) {
		return false
	}
	keys := []string{"dependency", "expected", "source", "target"}
	if version == MappedVersion {
		keys = append(keys, "target_path")
	}
	for _, raw := range entries {
		e, ok := exactKeys(raw, keys...)
		if !ok {
			return false
		}
		if _, ok = exactKeys(e["dependency"], "id", "service", "kind", "reference", "path"); !ok {
			return false
		}
		if _, ok = exactKeys(e["source"], "state", "type"); !ok {
			return false
		}
		if !bytes.Equal(bytes.TrimSpace(e["target"]), []byte("null")) {
			if _, ok = exactKeys(e["target"], "state", "type"); !ok {
				return false
			}
		}
	}
	return true
}

func WriteSnapshot(path string, s Snapshot) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return errors.New("cannot encode snapshot")
	}
	if len(data)+1 > MaxInputBytes {
		return errors.New("snapshot exceeds 16 MiB limit")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create output (existing files are never overwritten)")
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("cannot write snapshot; output may be incomplete")
	}
	return nil
}
