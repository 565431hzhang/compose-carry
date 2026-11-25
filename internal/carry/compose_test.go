package carry

import (
	"encoding/json"
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
