package carry

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
)

const ResourceScope = "Only service-referenced external volumes/networks in the selected Docker context; existence, not contents, drivers, connectivity, host paths, or application readiness."

type ResourceReference struct {
	Service string `json:"service"`
	Key     string `json:"key"`
}

type Resource struct {
	Kind       string              `json:"kind"`
	Name       string              `json:"name"`
	References []ResourceReference `json:"references"`
}

type QueryError string

const (
	QueryCommandNotFound QueryError = "command_not_found"
	QueryTimeout         QueryError = "timeout"
	QueryOutputLimit     QueryError = "output_limit"
	QueryCommandFailed   QueryError = "command_failed"
	QueryCanceled        QueryError = "query_canceled"
	QueryIOError         QueryError = "io_error"
)

func (QueryError) Error() string { return "Docker query unavailable" }

func queryReason(err error) string {
	var failure QueryError
	if errors.As(err, &failure) {
		switch failure {
		case QueryCommandNotFound, QueryTimeout, QueryOutputLimit, QueryCommandFailed, QueryCanceled, QueryIOError:
			return string(failure)
		}
	}
	return "query_failed"
}

func resourceHint(reason string) string {
	switch reason {
	case "command_not_found":
		return "Install Docker CLI and ensure it is available on PATH."
	case "timeout":
		return "Check the selected Docker context and daemon connectivity; retry when the daemon responds."
	case "output_limit":
		return "The Docker inventory exceeds 16 MiB; inspect it locally or use a context with a smaller inventory."
	case "command_failed":
		return "Check the selected context, daemon access and credentials with Docker CLI locally; raw diagnostics are not included."
	case "query_canceled":
		return "The query was canceled; retry when ready."
	case "io_error":
		return "Check Docker CLI executable permissions and local process or pipe failures."
	case "query_failed":
		return "Check the selected Docker context with Docker CLI locally; the query could not be verified."
	case "invalid_inventory":
		return "Check Docker CLI compatibility and projected list output locally; the inventory could not be parsed."
	case "ambiguous_reference":
		return "Use an unambiguous full network ID or resource name and regenerate the Compose JSON."
	case "unresolved_name":
		return "Render Compose variables and inspect the resource name before retrying."
	case "not_found":
		return "Verify the resource name and selected Docker context; resource contents and readiness are not checked."
	default:
		return ""
	}
}

type ResourceResult struct {
	Resource
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	Hint        string `json:"hint,omitempty"`
	CheckedName string `json:"checked_name"`
}

type ResourceSummary struct {
	Present    int `json:"present"`
	Missing    int `json:"missing"`
	Unverified int `json:"unverified"`
}

type ResourceReport struct {
	Scope         string           `json:"scope"`
	DockerContext string           `json:"docker_context"`
	Summary       ResourceSummary  `json:"summary"`
	ExitCode      int              `json:"exit_code"`
	Results       []ResourceResult `json:"results"`
}

func ParseResources(data []byte) ([]Resource, error) {
	resources, err := parseResources(data)
	if err != nil {
		return nil, errors.New("invalid external resource references; use rendered docker compose config --format json with boolean external definitions")
	}
	return resources, nil
}

func parseResources(data []byte) ([]Resource, error) {
	if err := checkJSON(data); err != nil {
		return nil, err
	}
	root, err := object(data)
	if err != nil {
		return nil, err
	}
	services, err := object(root["services"])
	if err != nil {
		return nil, err
	}
	found := map[string]*Resource{}
	definitionsByKind := map[string]map[string]json.RawMessage{}
	add := func(service, kind, key string, implicit bool) error {
		definitions, exists := root[kind+"s"]
		if !exists && implicit {
			return nil
		}
		defs := definitionsByKind[kind]
		if defs == nil {
			var err error
			defs, err = object(definitions)
			if err != nil {
				return err
			}
			definitionsByKind[kind] = defs
		}
		raw, exists := defs[key]
		if !exists {
			if implicit {
				return nil
			}
			return errors.New("unknown resource reference")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		def, err := object(raw)
		if err != nil {
			return err
		}
		external := false
		if raw, exists := def["external"]; exists {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &external) != nil {
				return errors.New("invalid external flag")
			}
		}
		if !external {
			return nil
		}
		name := key
		if _, exists := def["name"]; exists {
			name, err = text(def, "name", true)
			if err != nil {
				return err
			}
		}
		id, _ := json.Marshal([]string{kind, name})
		if found[string(id)] == nil {
			found[string(id)] = &Resource{Kind: kind, Name: name, References: []ResourceReference{}}
		}
		resource := found[string(id)]
		ref := ResourceReference{Service: service, Key: key}
		for _, existing := range resource.References {
			if existing == ref {
				return nil
			}
		}
		resource.References = append(resource.References, ref)
		return nil
	}
	for service, raw := range services {
		if service == "" {
			return nil, errors.New("empty service")
		}
		svc, err := object(raw)
		if err != nil {
			return nil, err
		}
		mounts, err := array(svc, "volumes")
		if err != nil {
			return nil, err
		}
		for _, raw := range mounts {
			mount, err := object(raw)
			if err != nil {
				return nil, err
			}
			kind, err := text(mount, "type", true)
			if err != nil {
				return nil, err
			}
			if kind != "volume" {
				continue
			}
			key, err := text(mount, "source", false)
			if err != nil {
				return nil, err
			}
			if key != "" {
				if err := add(service, "volume", key, false); err != nil {
					return nil, err
				}
			}
		}
		mode, err := text(svc, "network_mode", false)
		if err != nil {
			return nil, err
		}
		keys := []string{}
		if raw, exists := svc["networks"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			raw = bytes.TrimSpace(raw)
			if len(raw) > 0 && raw[0] == '[' {
				if json.Unmarshal(raw, &keys) != nil {
					return nil, errors.New("invalid network references")
				}
			} else {
				networks, err := object(raw)
				if err != nil {
					return nil, err
				}
				for key, value := range networks {
					if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
						if _, err := object(value); err != nil {
							return nil, err
						}
					}
					keys = append(keys, key)
				}
			}
		}
		if mode != "" {
			if len(keys) > 0 {
				return nil, errors.New("networks and network mode conflict")
			}
			continue
		}
		implicit := len(keys) == 0
		if implicit {
			keys = []string{"default"}
		}
		for _, key := range keys {
			if key == "" {
				return nil, errors.New("empty network reference")
			}
			if err := add(service, "network", key, implicit); err != nil {
				return nil, err
			}
		}
	}
	resources := make([]Resource, 0, len(found))
	for _, r := range found {
		sort.Slice(r.References, func(i, j int) bool {
			a, b := r.References[i], r.References[j]
			if a.Service != b.Service {
				return a.Service < b.Service
			}
			return a.Key < b.Key
		})
		resources = append(resources, *r)
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].Name < resources[j].Name
	})
	return resources, nil
}

type inventoryEntry struct{ name, id string }

func parseInventory(kind string, data []byte) ([]inventoryEntry, error) {
	invalid := errors.New("invalid inventory")
	if len(data) > MaxInputBytes {
		return nil, invalid
	}
	entries := []inventoryEntry{}
	ids := map[string]bool{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if checkJSON(line) != nil {
			return nil, invalid
		}
		entry := inventoryEntry{}
		if kind == "volume" {
			if json.Unmarshal(line, &entry.name) != nil || entry.name == "" {
				return nil, invalid
			}
		} else {
			var pair []string
			if json.Unmarshal(line, &pair) != nil || len(pair) != 2 || pair[1] == "" || pair[0] == "" || strings.Trim(pair[0], "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
				return nil, invalid
			}
			entry = inventoryEntry{name: pair[1], id: pair[0]}
			if ids[entry.id] {
				return nil, invalid
			}
			ids[entry.id] = true
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func unresolvedName(name string) bool {
	return name == "" || strings.Contains(name, "$") || strings.IndexFunc(name, unicode.IsControl) >= 0
}

func CheckResources(resources []Resource, dockerContext string, query func(string) ([]byte, error)) ResourceReport {
	return checkResources(resources, dockerContext, nil, query)
}

func CheckResourcesWithMap(resources []Resource, dockerContext string, mapping ResourceMap, query func(string) ([]byte, error)) (ResourceReport, error) {
	if err := mapping.Validate(resources); err != nil {
		return ResourceReport{}, err
	}
	return checkResources(resources, dockerContext, mapping, query), nil
}

func checkResources(resources []Resource, dockerContext string, mapping ResourceMap, query func(string) ([]byte, error)) ResourceReport {
	report := ResourceReport{Scope: ResourceScope, DockerContext: dockerContext, Results: []ResourceResult{}}
	inventories := map[string][]inventoryEntry{}
	reasons := map[string]string{}
	for _, kind := range []string{"volume", "network"} {
		needed := false
		for _, r := range resources {
			if r.Kind == kind && !unresolvedName(r.Name) {
				needed = true
			}
		}
		if !needed {
			continue
		}
		data, err := query(kind)
		if err != nil {
			reasons[kind] = queryReason(err)
			continue
		}
		inventory, err := parseInventory(kind, data)
		if err != nil {
			reasons[kind] = "invalid_inventory"
			continue
		}
		inventories[kind] = inventory
	}
	for _, r := range resources {
		result := ResourceResult{Resource: r, CheckedName: mapping.checkedName(r), Status: "unverified", Reason: reasons[r.Kind]}
		switch {
		case r.Kind != "volume" && r.Kind != "network":
			result.Reason = "unsupported_kind"
		case unresolvedName(r.Name):
			result.Reason = "unresolved_name"
		case result.Reason != "":
		default:
			matches := 0
			for _, entry := range inventories[r.Kind] {
				if entry.name == result.CheckedName || (r.Kind == "network" && strings.HasPrefix(entry.id, result.CheckedName)) {
					matches++
				}
			}
			switch matches {
			case 0:
				result.Status = "missing"
				result.Reason = "not_found"
			case 1:
				result.Status = "present"
				result.Reason = "found"
			default:
				result.Reason = "ambiguous_reference"
			}
		}
		result.Hint = resourceHint(result.Reason)
		switch result.Status {
		case "present":
			report.Summary.Present++
		case "missing":
			report.Summary.Missing++
		default:
			report.Summary.Unverified++
		}
		report.Results = append(report.Results, result)
	}
	if len(resources) == 0 || report.Summary.Missing > 0 || report.Summary.Unverified > 0 {
		report.ExitCode = 1
	}
	return report
}
