package carry

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sort"
)

const ResourceManifestKind = "compose-carry.resources"

type ResourceManifest struct {
	Kind      string     `json:"kind"`
	Version   int        `json:"version"`
	Resources []Resource `json:"resources"`
}

func (m ResourceManifest) Validate() error {
	invalid := errors.New("invalid resource manifest structure or references")
	if m.Kind != ResourceManifestKind || m.Version != 1 || m.Resources == nil {
		return invalid
	}
	seen := map[[2]string]bool{}
	references := map[[3]string]bool{}
	for _, r := range m.Resources {
		if (r.Kind != "volume" && r.Kind != "network") || r.Name == "" || len(r.References) == 0 {
			return invalid
		}
		identity := [2]string{r.Kind, r.Name}
		if seen[identity] {
			return invalid
		}
		seen[identity] = true
		for _, ref := range r.References {
			identity := [3]string{r.Kind, ref.Service, ref.Key}
			if ref.Service == "" || ref.Key == "" || references[identity] {
				return invalid
			}
			references[identity] = true
		}
	}
	return nil
}

func sortedManifest(m ResourceManifest) ResourceManifest {
	resources := make([]Resource, len(m.Resources))
	for i, r := range m.Resources {
		r.References = append([]ResourceReference(nil), r.References...)
		sort.Slice(r.References, func(i, j int) bool {
			a, b := r.References[i], r.References[j]
			if a.Service != b.Service {
				return a.Service < b.Service
			}
			return a.Key < b.Key
		})
		resources[i] = r
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].Name < resources[j].Name
	})
	m.Resources = resources
	return m
}

func DecodeResourceManifest(data []byte) (ResourceManifest, error) {
	var m ResourceManifest
	invalid := errors.New("invalid resource manifest JSON")
	if len(data) > MaxInputBytes || checkJSON(data) != nil {
		return m, invalid
	}
	root, ok := exactKeys(data, "kind", "version", "resources")
	if !ok {
		return m, invalid
	}
	var resources []json.RawMessage
	if json.Unmarshal(root["resources"], &resources) != nil || resources == nil {
		return m, invalid
	}
	for _, raw := range resources {
		obj, ok := exactKeys(raw, "kind", "name", "references")
		if !ok {
			return m, invalid
		}
		for _, key := range []string{"kind", "name"} {
			if _, err := text(obj, key, true); err != nil {
				return m, invalid
			}
		}
		var refs []json.RawMessage
		if json.Unmarshal(obj["references"], &refs) != nil || len(refs) == 0 {
			return m, invalid
		}
		for _, raw := range refs {
			ref, ok := exactKeys(raw, "service", "key")
			if !ok {
				return m, invalid
			}
			for _, key := range []string{"service", "key"} {
				if _, err := text(ref, key, true); err != nil {
					return m, invalid
				}
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil {
		return m, invalid
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	return sortedManifest(m), nil
}

func WriteResourceManifest(filename string, m ResourceManifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sortedManifest(m), "", "  ")
	if err != nil {
		return errors.New("cannot encode resource manifest")
	}
	if len(data)+1 > MaxInputBytes {
		return errors.New("resource manifest exceeds 16 MiB limit")
	}
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create resource manifest (existing files are never overwritten)")
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("cannot write resource manifest; output may be incomplete")
	}
	return nil
}

type ResourceMap map[string]map[string]string

func ParseResourceMap(data []byte) (ResourceMap, error) {
	invalid := errors.New("invalid resource map: expected volumes/networks objects with unique resolved name pairs")
	if len(data) > MaxInputBytes || checkJSON(data) != nil {
		return nil, invalid
	}
	root, err := object(data)
	if err != nil {
		return nil, invalid
	}
	result := ResourceMap{}
	for category, raw := range root {
		if category != "volumes" && category != "networks" {
			return nil, invalid
		}
		names, err := object(raw)
		if err != nil {
			return nil, invalid
		}
		result[category] = map[string]string{}
		for from := range names {
			to, err := text(names, from, true)
			if err != nil || unresolvedName(from) || unresolvedName(to) {
				return nil, invalid
			}
			result[category][from] = to
		}
	}
	return result, nil
}

func (paths ResourceMap) checkedName(r Resource) string {
	if mapped, ok := paths[r.Kind+"s"][r.Name]; ok {
		return mapped
	}
	return r.Name
}

func (paths ResourceMap) Validate(resources []Resource) error {
	invalid := errors.New("invalid resource map: unknown source, unresolved name or colliding target names")
	known := map[[2]string]bool{}
	for _, r := range resources {
		known[[2]string{r.Kind + "s", r.Name}] = true
	}
	for category, names := range paths {
		if category != "volumes" && category != "networks" {
			return invalid
		}
		for from, to := range names {
			if !known[[2]string{category, from}] || unresolvedName(from) || unresolvedName(to) {
				return invalid
			}
		}
	}
	targets := map[[2]string]bool{}
	for _, r := range resources {
		identity := [2]string{r.Kind, paths.checkedName(r)}
		if targets[identity] {
			return invalid
		}
		targets[identity] = true
	}
	return nil
}
