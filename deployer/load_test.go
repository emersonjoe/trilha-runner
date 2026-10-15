package deployer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example an operator copies to /etc/trilha-runner/delivery.json loads
// as shipped.
func TestLoadShippedExample(t *testing.T) {
	config, err := Load("../deploy/eoslab/delivery.json.example")
	if err != nil {
		t.Fatal(err)
	}
	compose, ok := config.Profiles["compose"]
	if !ok || len(compose.Steps) != 2 || compose.Migrate == nil || compose.Migrate.Reversible || len(compose.Health) != 2 {
		t.Fatalf("compose = %+v", compose)
	}
	if production := config.Profiles["production"]; len(production.Deploy) != 1 || production.HealthURL == "" || production.TimeoutSeconds != 600 {
		t.Fatalf("production = %+v", production)
	}
}

// A profile file that does not say exactly what the operator meant is
// refused at startup rather than deploying something else.
func TestLoadRefuses(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ name, content, want string }{
		{"misspelled field", `{"profiles":{"web":{"deploy":["/bin/true"],"rolback":["/bin/false"]}}}`, `unknown field "rolback"`},
		{"not JSON", `profiles: web`, "deployer: config"},
		{"no profiles", `{"profiles":{}}`, "no profiles configured"},
		{"empty object", `{}`, "no profiles configured"},
	}
	for _, c := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".json")
		os.WriteFile(path, []byte(c.content), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}
