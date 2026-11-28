package carry

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseResources(t *testing.T) {
	input := []byte(`{"name":"project","services":{"app":{"environment":{"TOKEN":"not-retained"},"volumes":[{"type":"volume","source":"db","target":"/db"},{"type":"volume","source":"db","target":"/again"},{"type":"volume","target":"/anonymous"},{"type":"volume","source":"managed","target":"/managed"},{"type":"bind","source":"/data","target":"/data"}],"networks":{"edge":null}},"worker":{"volumes":[{"type":"volume","source":"alias"}],"networks":["edge"]},"implicit":{},"host":{"network_mode":"host"}},"volumes":{"db":{"external":true,"name":"actual-db"},"alias":{"external":true,"name":"actual-db"},"managed":null,"unused":{"external":true}},"networks":{"edge":{"external":true,"name":"outside"},"default":{"external":true},"unused":{"external":true}}}`)
	resources, err := ParseResources(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []Resource{
		{Kind: "network", Name: "default", References: []ResourceReference{{Service: "implicit", Key: "default"}}},
		{Kind: "network", Name: "outside", References: []ResourceReference{{Service: "app", Key: "edge"}, {Service: "worker", Key: "edge"}}},
		{Kind: "volume", Name: "actual-db", References: []ResourceReference{{Service: "app", Key: "db"}, {Service: "worker", Key: "alias"}}},
	}
	if !reflect.DeepEqual(resources, want) {
		t.Fatalf("%+v", resources)
	}
	raw, _ := json.Marshal(resources)
	for _, secret := range []string{"not-retained", "unused", "managed", "project"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("retained unrelated input", secret)
		}
	}
}

func TestResourceNetworkForms(t *testing.T) {
	for _, networks := range []string{``, `,"networks":null`, `,"networks":{}`, `,"networks":[]`, `,"networks":["default"]`, `,"networks":{"default":{}}`} {
		input := `{"services":{"app":{"image":"example"` + networks + `}},"networks":{"default":{"external":true,"name":"edge"}}}`
		resources, err := ParseResources([]byte(input))
		if err != nil || len(resources) != 1 || resources[0].Name != "edge" {
			t.Fatal(input, resources, err)
		}
	}
	for _, input := range []string{`{"services":{}}`, `{"services":{"app":{}}}`, `{"services":{"app":{"network_mode":"none"}},"networks":{"default":{"external":true}}}`, `{"services":{"app":{"networks":["internal"]}},"networks":{"internal":{"external":false}}}`} {
		resources, err := ParseResources([]byte(input))
		if err != nil || resources == nil || len(resources) != 0 {
			t.Fatal(resources, err)
		}
	}
}

func TestRejectInvalidResources(t *testing.T) {
	for _, input := range []string{
		`null`, `{}`, `{"services":null}`, `{"services":{"":{}}}`, `{"services":{"app":null}}`,
		`{"services":{},"services":{}}`, `{"services":{}} {}`,
		`{"services":{"app":{"volumes":["db:/data"]}}}`,
		`{"services":{"app":{"volumes":[{"type":"volume","source":"unknown"}]}}}`,
		`{"services":{"app":{"networks":["unknown"]}}}`,
		`{"services":{"app":{"networks":[null]}}}`,
		`{"services":{"app":{"networks":{"edge":42}}}}`,
		`{"services":{"app":{"network_mode":"host","networks":["edge"]}}}`,
		`{"services":{"app":{"networks":["edge"]}},"networks":{"edge":{"external":"true"}}}`,
		`{"services":{"app":{"networks":["edge"]}},"networks":{"edge":{"external":null}}}`,
		`{"services":{"app":{"networks":["edge"]}},"networks":{"edge":{"external":{"name":"old"}}}}`,
		`{"services":{"app":{"networks":["edge"]}},"networks":{"edge":{"external":true,"name":null}}}`,
		`{"services":{"app":{"networks":["edge"]}},"networks":{"edge":{"external":true,"name":""}}}`,
	} {
		if _, err := ParseResources([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestResourceDefinitionLookupBoundaries(t *testing.T) {
	for _, input := range []string{
		`{"services":{"app":{"network_mode":"none"}},"volumes":null,"networks":42}`,
		`{"services":{"app":{"volumes":[{"type":"volume","source":"x"}],"networks":["x"]}},"volumes":{"x":{"external":true},"unused":42},"networks":{"x":{"external":true},"unused":false}}`,
		`{"services":{"app":{"networks":["x"]},"implicit":{}},"networks":{"x":{"external":true}}}`,
	} {
		if _, err := ParseResources([]byte(input)); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{
		`{"services":{"app":{"networks":["x"]}},"networks":null}`,
		`{"services":{"app":{"networks":["x","missing"]}},"networks":{"x":{"external":true}}}`,
		`{"services":{"app":{"volumes":[{"type":"volume","source":"x"}],"networks":["x"]}},"volumes":{"x":{"external":true}}}`,
	} {
		if _, err := ParseResources([]byte(input)); err == nil {
			t.Fatal("accepted invalid referenced definition")
		}
	}
	for _, name := range []string{"first", "second"} {
		data := []byte(`{"services":{"app":{"networks":["x"]}},"networks":{"x":{"external":true,"name":"` + name + `"}}}`)
		resources, err := ParseResources(data)
		if err != nil || len(resources) != 1 || resources[0].Name != name {
			t.Fatalf("definitions leaked across inputs: %v %v", resources, err)
		}
	}
}

func TestResourceInventoryMatching(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	tests := []struct{ kind, name, data, status, reason string }{
		{"volume", "data", "\"data\"\n\"other\"\n", "present", "found"},
		{"volume", "data", "", "missing", "not_found"},
		{"volume", "dat", "\"data\"\n", "missing", "not_found"},
		{"volume", "data", "\"data\"\n\"data\"\n", "unverified", "ambiguous_reference"},
		{"volume", "data", "null\n", "unverified", "invalid_inventory"},
		{"volume", "data", "\"data\"\nnot JSON", "unverified", "invalid_inventory"},
		{"volume", "data", "\"data\" \"extra\"", "unverified", "invalid_inventory"},
		{"network", "edge", `["` + a + `","edge"]` + "\n", "present", "found"},
		{"network", a, `["` + a + `","edge"]` + "\n", "present", "found"},
		{"network", a[:12], `["` + a + `","edge"]` + "\n", "present", "found"},
		{"network", "unknown", `["` + a + `","edge"]` + "\n", "missing", "not_found"},
		{"network", "edge", `["` + a + `","edge"]` + "\n" + `["` + b + `","edge"]`, "unverified", "ambiguous_reference"},
		{"network", a[:12], `["` + a + `","edge"]` + "\n" + `["` + b + `","` + a[:12] + `"]`, "unverified", "ambiguous_reference"},
		{"network", "aaa", `["` + a + `","one"]` + "\n" + `["aaa` + strings.Repeat("c", 61) + `","two"]`, "unverified", "ambiguous_reference"},
		{"network", "edge", `["","edge"]`, "unverified", "invalid_inventory"},
		{"network", "edge", `["qj46testswarmid12345678901","edge"]`, "present", "found"},
		{"network", "edge", `["` + a + `","edge"]` + "\n" + `["` + a + `","other"]`, "unverified", "invalid_inventory"},
		{"network", "edge", `{"ID":"` + a + `","Name":"edge"}`, "unverified", "invalid_inventory"},
	}
	for _, tc := range tests {
		t.Run(tc.kind+"/"+tc.reason+"/"+tc.name, func(t *testing.T) {
			calls := 0
			report := CheckResources([]Resource{{Kind: tc.kind, Name: tc.name, References: []ResourceReference{}}}, "test", func(kind string) ([]byte, error) {
				calls++
				if kind != tc.kind {
					t.Fatal(kind)
				}
				return []byte(tc.data), nil
			})
			if calls != 1 || len(report.Results) != 1 || report.Results[0].Status != tc.status || report.Results[0].Reason != tc.reason {
				t.Fatal(report)
			}
			if tc.status == "present" && report.Results[0].Hint != "" {
				t.Fatal("unnecessary success hint")
			}
			if tc.status != "present" && report.Results[0].Hint == "" {
				t.Fatal("missing diagnostic hint")
			}
			want := 1
			if tc.status == "present" {
				want = 0
			}
			if report.ExitCode != want {
				t.Fatal(report)
			}
		})
	}
}

func TestResourceQueryFailures(t *testing.T) {
	resources := []Resource{{Kind: "volume", Name: "data"}, {Kind: "volume", Name: "other"}, {Kind: "network", Name: "edge"}}
	calls := map[string]int{}
	report := CheckResources(resources, "selected", func(kind string) ([]byte, error) {
		calls[kind]++
		if kind == "volume" {
			return []byte("\"data\"\n"), errors.New("private-diagnostic")
		}
		return []byte(`["` + strings.Repeat("a", 64) + `","edge"]`), nil
	})
	if calls["volume"] != 1 || calls["network"] != 1 || report.Summary != (ResourceSummary{Present: 1, Unverified: 2}) || report.ExitCode != 1 {
		t.Fatal(report, calls)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "private-diagnostic") {
		t.Fatal("leaked diagnostic")
	}
	report = CheckResources(resources[:1], "test", func(string) ([]byte, error) { return []byte(strings.Repeat(" ", MaxInputBytes+1)), nil })
	if report.Results[0].Reason != "invalid_inventory" {
		t.Fatal(report)
	}
	for _, rs := range [][]Resource{nil, {{Kind: "network", Name: "${NETWORK}"}}, {{Kind: "volume", Name: "bad\nname"}}} {
		report = CheckResources(rs, "test", func(string) ([]byte, error) { t.Fatal("unexpected query"); return nil, nil })
		if report.ExitCode != 1 || report.Results == nil {
			t.Fatal(report)
		}
	}
}
