package carry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

type Dependency struct {
	ID        string `json:"id"`
	Service   string `json:"service"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Path      string `json:"path"`
}

func dependency(service, kind, reference, path string) Dependency {
	d := Dependency{Service: service, Kind: kind, Reference: reference, Path: path}
	identity, _ := json.Marshal([]string{service, kind, reference, path})
	sum := sha256.Sum256(identity)
	d.ID = hex.EncodeToString(sum[:])
	return d
}

// Reject duplicate keys instead of accepting the last value of an ambiguous input.
func checkJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return errors.New("JSON nesting exceeds limit")
		}
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate or invalid JSON key")
				}
				keys[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, err = dec.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func object(data json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return nil, errors.New("expected object")
	}
	return obj, nil
}

func text(obj map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, exists := obj[key]
	if !exists {
		if required {
			return "", errors.New("missing field")
		}
		return "", nil
	}
	var s string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &s) != nil || (required && s == "") {
		return "", errors.New("invalid string field")
	}
	return s, nil
}

func array(obj map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	raw, exists := obj[key]
	if !exists {
		return nil, nil
	}
	var a []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &a) != nil {
		return nil, errors.New("invalid array")
	}
	return a, nil
}

func ParseCompose(data []byte) ([]Dependency, error) {
	deps, err := parseCompose(data)
	if err != nil {
		return nil, errors.New("invalid Compose JSON; use docker compose config --format json and check service dependency definitions")
	}
	return deps, nil
}

func parseCompose(data []byte) ([]Dependency, error) {
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
	deps := []Dependency{}
	definitionsByKind := map[string]map[string]json.RawMessage{}
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
		for i, raw := range mounts {
			mount, err := object(raw)
			if err != nil {
				return nil, err
			}
			kind, err := text(mount, "type", true)
			if err != nil {
				return nil, err
			}
			target, err := text(mount, "target", true)
			if err != nil {
				return nil, err
			}
			reference := fmt.Sprintf("volumes[%d]:%s", i, target)
			if kind != "bind" {
				deps = append(deps, dependency(service, "excluded", reference, ""))
				continue
			}
			source, err := text(mount, "source", true)
			if err != nil {
				return nil, err
			}
			deps = append(deps, dependency(service, "bind", reference, source))
		}
		for _, field := range []string{"configs", "secrets"} {
			refs, err := array(svc, field)
			if err != nil {
				return nil, err
			}
			for i, raw := range refs {
				var name string
				if err := json.Unmarshal(raw, &name); err != nil {
					ref, err := object(raw)
					if err != nil {
						return nil, err
					}
					name, err = text(ref, "source", true)
					if err != nil {
						return nil, err
					}
				}
				if name == "" {
					return nil, errors.New("empty reference")
				}
				definitions := definitionsByKind[field]
				if definitions == nil {
					definitions, err = object(root[field])
					if err != nil {
						return nil, err
					}
					definitionsByKind[field] = definitions
				}
				def, err := object(definitions[name])
				if err != nil {
					return nil, err
				}
				reference := fmt.Sprintf("%s[%d]:%s", field, i, name)
				path, err := text(def, "file", false)
				if err != nil {
					return nil, err
				}
				excluded := false
				if external, ok := def["external"]; ok && !bytes.Equal(bytes.TrimSpace(external), []byte("false")) {
					excluded = true
				}
				for _, key := range []string{"environment", "content", "driver", "template_driver"} {
					if _, ok := def[key]; ok {
						excluded = true
					}
				}
				if excluded {
					deps = append(deps, dependency(service, "excluded", reference, ""))
					continue
				}
				if path == "" {
					return nil, errors.New("missing dependency file")
				}
				kind := "config"
				if field == "secrets" {
					kind = "secret"
				}
				deps = append(deps, dependency(service, kind, reference, path))
			}
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].ID < deps[j].ID })
	return deps, nil
}
