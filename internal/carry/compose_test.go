package carry

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseCompose(t *testing.T) {
	data := []byte(`{"services":{"app":{"environment":{"PASSWORD":"do-not-export-me"},"volumes":[{"type":"bind","source":"/srv/data","target":"/data"},{"type":"volume","source":"db","target":"/db"}],"configs":["settings"],"secrets":[{"source":"password","target":"not-a-host-path"},"external"]},"worker":{"configs":[{"source":"settings"}]}},"configs":{"settings":{"file":"/srv/config.json"},"unused":{"file":"/not-used"}},"secrets":{"password":{"file":"/srv/password"},"external":{"external":true}}}`)
	deps, err := ParseCompose(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 6 {
		t.Fatalf("dependencies: %d", len(deps))
	}
	counts := map[string]int{}
	for _, dep := range deps {
		counts[dep.Kind]++
		if dep.ID != dependency(dep.Service, dep.Kind, dep.Reference, dep.Path).ID {
			t.Fatal("unstable identity")
		}
	}
	if counts["bind"] != 1 || counts["config"] != 2 || counts["secret"] != 1 || counts["excluded"] != 2 {
		t.Fatal(counts)
	}
	encoded, _ := json.Marshal(deps)
	for _, secret := range []string{"do-not-export-me", "not-a-host-path", "/not-used"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("unexpected retained data: %s", secret)
		}
	}
}

func TestInvalidCompose(t *testing.T) {
	cases := []string{
		`null`, `{}`, `{"services":null}`, `{"services":{"app":null}}`,
		`{"services":{},"services":{}}`, `{"services":{}} {}`,
		`{"services":{"app":{"volumes":["/srv:/data"]}}}`,
		`{"services":{"app":{"volumes":[{"type":"bind","target":"/data"}]}}}`,
		`{"services":{"app":{"volumes":null}}}`,
		`{"services":{"app":{"configs":["missing"]}}}`,
		`{"services":{"app":{"secrets":[null]}}}`,
		`{"services":{"app":{"configs":["x"]}},"configs":{"x":{"file":null}}}`,
	}
	for _, input := range cases {
		if _, err := ParseCompose([]byte(input)); err == nil {
			t.Errorf("accepted invalid input: %s", input)
		}
	}
}

func TestComposeDefinitionLookupBoundaries(t *testing.T) {
	for _, input := range []string{
		`{"services":{"app":{}},"configs":null,"secrets":42}`,
		`{"services":{"app":{"configs":["shared"],"secrets":["shared"]}},"configs":{"shared":{"file":"/config"},"unused":42},"secrets":{"shared":{"file":"/secret"},"unused":false}}`,
	} {
		if _, err := ParseCompose([]byte(input)); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{
		`{"services":{"app":{"configs":["x"]}},"configs":null}`,
		`{"services":{"app":{"configs":["x","missing"]}},"configs":{"x":{"file":"/x"}}}`,
		`{"services":{"app":{"configs":["x"],"secrets":["x"]}},"configs":{"x":{"file":"/x"}}}`,
	} {
		if _, err := ParseCompose([]byte(input)); err == nil {
			t.Fatal("accepted invalid referenced definition")
		}
	}
	for _, path := range []string{"/first", "/second"} {
		data := []byte(`{"services":{"app":{"configs":["x"]}},"configs":{"x":{"file":"` + path + `"}}}`)
		deps, err := ParseCompose(data)
		if err != nil || len(deps) != 1 || deps[0].Path != path {
			t.Fatalf("definitions leaked across inputs: %v %v", deps, err)
		}
	}
}

func TestRelativeAndExcludedDependencies(t *testing.T) {
	deps, err := ParseCompose([]byte(`{"services":{"app":{"volumes":[{"type":"bind","source":"./data","target":"/data"}],"configs":["inline","env"]}},"configs":{"inline":{"content":"sensitive-inline"},"env":{"environment":"PRIVATE_VALUE"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 3 {
		t.Fatal(deps)
	}
	raw, _ := json.Marshal(deps)
	if strings.Contains(string(raw), "sensitive-inline") || strings.Contains(string(raw), "PRIVATE_VALUE") {
		t.Fatal("input values leaked")
	}
}

func TestNoDependencies(t *testing.T) {
	deps, err := ParseCompose([]byte(`{"services":{"app":{"image":"example"}}}`))
	if err != nil || len(deps) != 0 {
		t.Fatalf("%v %v", deps, err)
	}
}

func definitionFixture(t testing.TB, kind string, count int) []byte {
	t.Helper()
	services := map[string]any{}
	definitions := map[string]any{}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("item-%04d", i)
		svc := map[string]any{"network_mode": "none"}
		switch kind {
		case "config", "secret":
			svc[kind+"s"] = []any{map[string]any{"source": key}}
			definitions[key] = map[string]any{"file": "/synthetic/" + key}
		case "volume":
			svc["volumes"] = []any{map[string]any{"type": "volume", "source": key, "target": "/data"}}
			definitions[key] = map[string]any{"external": true, "name": "external-" + key}
		case "network":
			delete(svc, "network_mode")
			svc["networks"] = map[string]any{key: nil}
			definitions[key] = map[string]any{"external": true, "name": "external-" + key}
		default:
			t.Fatalf("unknown fixture kind %q", kind)
		}
		services[key] = svc
	}
	data, err := json.Marshal(map[string]any{"services": services, kind + "s": definitions})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLargeDefinitionTables(t *testing.T) {
	const count = 320
	for _, kind := range []string{"config", "secret", "volume", "network"} {
		t.Run(kind, func(t *testing.T) {
			data := definitionFixture(t, kind, count)
			if kind == "config" || kind == "secret" {
				deps, err := ParseCompose(data)
				if err != nil || len(deps) != count {
					t.Fatalf("dependencies=%d error=%v", len(deps), err)
				}
				seen := map[string]bool{}
				for _, d := range deps {
					want := dependency(d.Service, kind, kind+"s[0]:"+d.Service, "/synthetic/"+d.Service)
					if d != want || seen[d.Service] {
						t.Fatalf("incorrect dependency: %+v", d)
					}
					seen[d.Service] = true
				}
			} else {
				resources, err := ParseResources(data)
				if err != nil || len(resources) != count {
					t.Fatalf("resources=%d error=%v", len(resources), err)
				}
				for i, r := range resources {
					key := fmt.Sprintf("item-%04d", i)
					if r.Kind != kind || r.Name != "external-"+key || len(r.References) != 1 || r.References[0] != (ResourceReference{Service: key, Key: key}) {
						t.Fatalf("incorrect resource: %+v", r)
					}
				}
			}
		})
	}
}

func TestDefinitionTableAllocationGrowth(t *testing.T) {
	for _, kind := range []string{"config", "secret", "volume", "network"} {
		t.Run(kind, func(t *testing.T) {
			allocations := func(count int) float64 {
				data := definitionFixture(t, kind, count)
				return testing.AllocsPerRun(1, func() {
					var err error
					if kind == "config" || kind == "secret" {
						_, err = ParseCompose(data)
					} else {
						_, err = ParseResources(data)
					}
					if err != nil {
						t.Fatal(err)
					}
				})
			}
			small, large := allocations(32), allocations(320)
			// Ten times the input must not repeatedly decode the entire definition table.
			if large > 20*small {
				t.Fatalf("allocation growth %.1fx exceeds 20x for 10x input (%.0f -> %.0f)", large/small, small, large)
			}
		})
	}
}

func BenchmarkDefinitionTables(b *testing.B) {
	for _, kind := range []string{"config", "secret", "volume", "network"} {
		for _, count := range []int{100, 1000} {
			b.Run(fmt.Sprintf("%s/%d", kind, count), func(b *testing.B) {
				data := definitionFixture(b, kind, count)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					var err error
					if kind == "config" || kind == "secret" {
						_, err = ParseCompose(data)
					} else {
						_, err = ParseResources(data)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
