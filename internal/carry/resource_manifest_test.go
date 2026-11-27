package carry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func resourceManifestFixture() ResourceManifest {
	return ResourceManifest{Kind: ResourceManifestKind, Version: 1, Resources: []Resource{
		{Kind: "volume", Name: "old", References: []ResourceReference{{Service: "app", Key: "data"}}},
		{Kind: "network", Name: "old", References: []ResourceReference{{Service: "app", Key: "edge"}}},
	}}
}

func TestResourceManifestRoundTrip(t *testing.T) {
	m := resourceManifestFixture()
	before, _ := json.Marshal(m)
	root := t.TempDir()
	a, b := filepath.Join(root, "a.json"), filepath.Join(root, "b.json")
	if err := WriteResourceManifest(a, m); err != nil {
		t.Fatal(err)
	}
	raw, err := ReadInput(a)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResourceManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Resources[0].Kind != "network" {
		t.Fatal("not sorted")
	}
	if err := WriteResourceManifest(b, decoded); err != nil {
		t.Fatal(err)
	}
	second, _ := ReadInput(b)
	if string(raw) != string(second) {
		t.Fatal("unstable serialization")
	}
	if err := WriteResourceManifest(a, m); err == nil {
		t.Fatal("overwritten manifest")
	}
	after, _ := json.Marshal(m)
	if string(before) != string(after) {
		t.Fatal("mutated input")
	}
	m.Resources = []Resource{}
	data, _ := json.Marshal(m)
	decoded, err = DecodeResourceManifest(data)
	if err != nil || decoded.Resources == nil || len(decoded.Resources) != 0 {
		t.Fatal(decoded, err)
	}
}

func TestInvalidResourceManifests(t *testing.T) {
	m := resourceManifestFixture()
	raw, _ := json.Marshal(m)
	for _, input := range []string{
		`null`, `{}`, string(raw) + `{}`,
		strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(raw), `"version":1`, `"version":2`, 1),
		strings.Replace(string(raw), `"version":1`, `"Version":1`, 1),
		strings.Replace(string(raw), `"name":"old"`, `"name":null`, 1),
		strings.Replace(string(raw), `"key":"data"`, `"key":"data","secret":"private"`, 1),
		strings.Replace(string(raw), `"kind":"volume"`, `"kind":"bind"`, 1),
		strings.Replace(string(raw), `"key":"data"`, `"key":null`, 1),
		`{"kind":"compose-carry.resources","version":1,"resources":null}`,
	} {
		if _, err := DecodeResourceManifest([]byte(input)); err == nil {
			t.Errorf("accepted invalid manifest %s", input)
		}
	}
	for _, change := range []func(*ResourceManifest){
		func(m *ResourceManifest) { m.Resources = nil },
		func(m *ResourceManifest) { m.Kind = "path-snapshot" },
		func(m *ResourceManifest) { m.Resources[0].Name = "" },
		func(m *ResourceManifest) { m.Resources[0].References = nil },
		func(m *ResourceManifest) { m.Resources[0].References = []ResourceReference{{Service: "", Key: "x"}} },
		func(m *ResourceManifest) {
			m.Resources[0].References = append(m.Resources[0].References, m.Resources[0].References[0])
		},
		func(m *ResourceManifest) { m.Resources = append(m.Resources, m.Resources[0]) },
		func(m *ResourceManifest) {
			r := m.Resources[0]
			r.Name = "different"
			m.Resources = append(m.Resources, r)
		},
	} {
		m := resourceManifestFixture()
		change(&m)
		raw, _ := json.Marshal(m)
		if _, err := DecodeResourceManifest(raw); err == nil {
			t.Fatal("accepted invalid state", string(raw))
		}
	}
	m = resourceManifestFixture()
	m.Resources[0].Name = strings.Repeat("x", MaxInputBytes)
	output := filepath.Join(t.TempDir(), "oversized.json")
	if err := WriteResourceManifest(output, m); err == nil {
		t.Fatal("oversized manifest written")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("created oversized output")
	}
}

func TestResourceMapValidation(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"unknown":{}}`, `{"volumes":null}`, `{"volumes":[]}`, `{"volumes":{"old":null}}`, `{"volumes":{"old":"x","old":"y"}}`, `{"volumes":{"old":""}}`, `{"volumes":{"${OLD}":"x"}}`, `{"networks":{"old":"${NEW}"}}`, `{"volumes":{"old":"bad\nname"}}`} {
		if _, err := ParseResourceMap([]byte(raw)); err == nil {
			t.Fatal("accepted map", raw)
		}
	}
	resources := resourceManifestFixture().Resources
	resources = append(resources, Resource{Kind: "volume", Name: "kept", References: []ResourceReference{{Service: "app", Key: "other"}}})
	for _, mapping := range []ResourceMap{
		{"volumes": {"unknown": "x"}},
		{"volumes": {"old": "kept"}},
		{"volumes": {"old": "x", "kept": "x"}},
		{"unknown": {}},
	} {
		calls := 0
		if _, err := CheckResourcesWithMap(resources, "test", mapping, func(string) ([]byte, error) { calls++; return nil, nil }); err == nil || calls != 0 {
			t.Fatal("invalid mapping queried Docker")
		}
	}
	if err := (ResourceMap{"volumes": {"old": "same"}, "networks": {"old": "same"}}).Validate(resources); err != nil {
		t.Fatal("different kinds must not collide", err)
	}
	unresolved := []Resource{{Kind: "volume", Name: "${OLD}"}}
	if err := (ResourceMap{"volumes": {"${OLD}": "valid"}}).Validate(unresolved); err == nil {
		t.Fatal("mapped unresolved source")
	}
}

func TestMappedResourceFailures(t *testing.T) {
	resources := []Resource{{Kind: "network", Name: "old", References: []ResourceReference{{Service: "app", Key: "edge"}}}}
	mapping := ResourceMap{"networks": {"old": "new"}}
	for _, tc := range []struct {
		data     string
		queryErr error
		reason   string
	}{
		{"", nil, "not_found"},
		{`["` + strings.Repeat("a", 64) + `","new"]` + "\n" + `["` + strings.Repeat("b", 64) + `","new"]`, nil, "ambiguous_reference"},
		{"private-partial-output", QueryTimeout, "timeout"},
	} {
		report, err := CheckResourcesWithMap(resources, "target", mapping, func(string) ([]byte, error) { return []byte(tc.data), tc.queryErr })
		if err != nil || report.ExitCode != 1 || len(report.Results) != 1 {
			t.Fatal(report, err)
		}
		r := report.Results[0]
		if r.Name != "old" || r.CheckedName != "new" || r.Reason != tc.reason || r.Hint == "" {
			t.Fatal(r)
		}
		if tc.reason != "not_found" && r.Status != "unverified" {
			t.Fatal("failure misreported missing", r)
		}
	}
}

func TestCheckMappedResources(t *testing.T) {
	resources := resourceManifestFixture().Resources
	before, _ := json.Marshal(resources)
	mapping, err := ParseResourceMap([]byte(`{"volumes":{"old":"new-volume"},"networks":{"old":"aaaaaaaaaaaa"}}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	report, err := CheckResourcesWithMap(resources, "target", mapping, func(kind string) ([]byte, error) {
		calls[kind]++
		if kind == "volume" {
			return []byte(`"new-volume"`), nil
		}
		return []byte(`["` + strings.Repeat("a", 64) + `","new-network"]`), nil
	})
	if err != nil || report.ExitCode != 0 || report.Summary.Present != 2 || !reflect.DeepEqual(calls, map[string]int{"volume": 1, "network": 1}) {
		t.Fatal(report, err, calls)
	}
	for _, r := range report.Results {
		if r.Name != "old" || r.CheckedName == "old" {
			t.Fatal("lost source or target name", r)
		}
	}
	after, _ := json.Marshal(resources)
	if string(before) != string(after) {
		t.Fatal("modified resource identity")
	}
	report, err = CheckResourcesWithMap(resources, "target", nil, func(kind string) ([]byte, error) {
		if kind == "volume" {
			return []byte(`"old"`), nil
		}
		return []byte(`["` + strings.Repeat("b", 64) + `","old"]`), nil
	})
	if err != nil || report.ExitCode != 0 {
		t.Fatal(report, err)
	}
	for _, r := range report.Results {
		if r.CheckedName != r.Name {
			t.Fatal("unmapped name changed")
		}
	}
	report, err = CheckResourcesWithMap([]Resource{{Kind: "volume", Name: "${MISSING}"}}, "test", nil, func(string) ([]byte, error) { t.Fatal("queried unresolved resource"); return nil, nil })
	if err != nil || report.Results[0].Reason != "unresolved_name" {
		t.Fatal(report, err)
	}
}
