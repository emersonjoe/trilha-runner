package deployer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/queue"
)

func TestMain(m *testing.M) {
	// The tests poll a local server; production waits a second between tries.
	healthInterval = 10 * time.Millisecond
	os.Exit(m.Run())
}

// script writes an executable that records that it ran and exits with code.
func script(t *testing.T, root, name string, code int) []string {
	t.Helper()
	path := filepath.Join(root, name)
	body := "#!/bin/sh\necho \"$TRILHA_STEP ran revision $TRILHA_REVISION\" >> " +
		filepath.Join(root, "order.txt") + "\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

func order(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "order.txt"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if name, _, _ := strings.Cut(line, " "); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func work(action string) queue.DeploymentWork {
	return queue.DeploymentWork{Deployment: queue.Deployment{
		ID: "deploy-1", Project: "app", Environment: "production",
		Profile: "compose", Action: action, Revision: "abc123",
	}}
}

func TestExecuteUsesAllowListAndRedactsSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	path := filepath.Join(root, "deploy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s %s' \"$TRILHA_REVISION\" \"$DATABASE_URL\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{Profiles: map[string]Profile{"compose": {Deploy: []string{path}}}}
	w := work("deploy")
	w.Secrets = map[string]string{"DATABASE_URL": "private-value"}
	result := config.Execute(context.Background(), w)
	if !result.Passed {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.Log, "private-value") || !strings.Contains(result.Log, "[REDACTED]") {
		t.Fatalf("secret leaked: %q", result.Log)
	}
	// Every step's exit and duration are in the log.
	if !strings.Contains(result.Log, "deploy: exit 0 in ") {
		t.Fatalf("log = %q", result.Log)
	}
}

func TestExecuteRejectsUnknownProfile(t *testing.T) {
	result := (Config{Profiles: map[string]Profile{}}).Execute(context.Background(), work("deploy"))
	if result.Passed || result.Error == "" {
		t.Fatalf("result = %#v", result)
	}
}

// A migration that fails aborts the delivery before traffic moves.
func TestMigrationFailureAbortsBeforeTheSwitch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	profile := Profile{
		Steps: []Step{
			{Name: "pull", Argv: script(t, root, "pull", 0)},
			{Name: "switch", Argv: script(t, root, "switch", 0), Switch: true},
		},
		Migrate:  &Migrate{Argv: script(t, root, "migrate", 1)},
		Rollback: script(t, root, "rollback", 0),
		Health:   []Health{{Name: "api", URL: "http://127.0.0.1:1/never", TimeoutSeconds: 1}},
	}
	result := Config{Profiles: map[string]Profile{"compose": profile}}.Execute(context.Background(), work("deploy"))
	if result.Passed || !strings.Contains(result.Error, "migration failed before the switch") {
		t.Fatalf("result = %#v", result)
	}
	if got := order(t, root); len(got) != 2 || got[0] != "pull" || got[1] != "migrate" {
		t.Fatalf("ran %v, want pull then migrate and nothing else", got)
	}
	if !strings.Contains(result.Log, "aborted before the switch") {
		t.Fatalf("log = %q", result.Log)
	}
}

// The happy path: pull, migrate, switch, then each service's health.
func TestStepsRunInOrderWithTheMigrationBeforeTheSwitch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer api.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer web.Close()
	profile := Profile{
		Steps: []Step{
			{Name: "pull", Argv: script(t, root, "pull", 0)},
			{Name: "switch", Argv: script(t, root, "switch", 0), Switch: true},
		},
		Migrate: &Migrate{Argv: script(t, root, "migrate", 0), Downgrade: script(t, root, "downgrade", 0), Reversible: true},
		Health: []Health{
			{Name: "api", URL: api.URL + "/health", TimeoutSeconds: 2},
			{Name: "web", URL: web.URL, TimeoutSeconds: 2},
		},
	}
	result := Config{Profiles: map[string]Profile{"compose": profile}}.Execute(context.Background(), work("deploy"))
	if !result.Passed || result.Error != "" {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(order(t, root), " "); got != "pull migrate switch" {
		t.Fatalf("ran %q", got)
	}
	if !strings.Contains(result.Health, "api=200 OK") || !strings.Contains(result.Health, "web=204") {
		t.Fatalf("health = %q", result.Health)
	}
	if !strings.Contains(result.Log, "migrate (reversible): exit 0") {
		t.Fatalf("log = %q", result.Log)
	}
}

// A service that never comes up triggers a rollback.
func TestHealthFailureTriggersRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer sick.Close()
	profile := Profile{
		Steps:    []Step{{Name: "switch", Argv: script(t, root, "switch", 0), Switch: true}},
		Migrate:  &Migrate{Argv: script(t, root, "migrate", 0), Downgrade: script(t, root, "downgrade", 0), Reversible: true},
		Rollback: script(t, root, "rollback", 0),
		Health:   []Health{{Name: "api", URL: sick.URL, TimeoutSeconds: 1}},
	}
	result := Config{Profiles: map[string]Profile{"compose": profile}}.Execute(context.Background(), work("deploy"))
	if result.Passed || !strings.Contains(result.Error, "health check api failed") {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(order(t, root), " "); got != "migrate switch rollback downgrade" {
		t.Fatalf("ran %q", got)
	}
	if result.Rollback != "image and schema" {
		t.Fatalf("rollback = %q", result.Rollback)
	}
}

// A migration that cannot be reversed makes the rollback image-only, and the
// log says why rather than pretending the schema went back.
func TestNonReversibleMigrationRollsBackTheImageOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	profile := Profile{
		Steps:    []Step{{Name: "switch", Argv: script(t, root, "switch", 0), Switch: true}},
		Migrate:  &Migrate{Argv: script(t, root, "migrate", 0)},
		Rollback: script(t, root, "rollback", 0),
	}
	result := Config{Profiles: map[string]Profile{"compose": profile}}.Execute(context.Background(), work("rollback"))
	if !result.Passed {
		t.Fatalf("result = %#v", result)
	}
	if result.Rollback != "image only: the migration is not reversible" {
		t.Fatalf("rollback = %q", result.Rollback)
	}
	if !strings.Contains(result.Log, "the schema keeps the new shape") {
		t.Fatalf("log = %q", result.Log)
	}
	if got := strings.Join(order(t, root), " "); got != "rollback" {
		t.Fatalf("ran %q: a non-reversible migration must not be downgraded", got)
	}
}

// on_fail: continue is the only way past a failing step, and it is recorded.
func TestStepOnFailContinue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	profile := Profile{Steps: []Step{
		{Name: "prune", Argv: script(t, root, "prune", 3), OnFail: "continue"},
		{Name: "switch", Argv: script(t, root, "switch", 0), Switch: true},
	}}
	result := Config{Profiles: map[string]Profile{"compose": profile}}.Execute(context.Background(), work("deploy"))
	if !result.Passed {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(order(t, root), " "); got != "prune switch" {
		t.Fatalf("ran %q", got)
	}
	if !strings.Contains(result.Log, "prune: exit 3") || !strings.Contains(result.Log, "the profile says continue") {
		t.Fatalf("log = %q", result.Log)
	}
	// Without on_fail, the same step aborts.
	root2 := t.TempDir()
	strict := Profile{Steps: []Step{
		{Name: "prune", Argv: script(t, root2, "prune", 3)},
		{Name: "switch", Argv: script(t, root2, "switch", 0), Switch: true},
	}}
	result = Config{Profiles: map[string]Profile{"compose": strict}}.Execute(context.Background(), work("deploy"))
	if result.Passed || !strings.Contains(result.Error, "step prune failed") {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(order(t, root2), " "); got != "prune" {
		t.Fatalf("ran %q", got)
	}
}

// The example shipped for eoslab is a valid configuration.
func TestLoadValidatesAndAcceptsTheShippedExample(t *testing.T) {
	config, err := Load(filepath.Join("..", "deploy", "eoslab", "delivery.json.example"))
	if err != nil {
		t.Fatal(err)
	}
	compose, ok := config.Profiles["compose"]
	if !ok {
		t.Fatalf("profiles = %v", config.Profiles)
	}
	if len(compose.Steps) == 0 || compose.Migrate == nil || len(compose.Health) == 0 {
		t.Fatalf("compose = %#v", compose)
	}
	var switches int
	for _, step := range compose.Steps {
		if step.Switch {
			switches++
		}
	}
	if switches != 1 {
		t.Fatalf("%d steps switch traffic, want exactly 1", switches)
	}
}

func TestLoadRefusesAnUnrunnableProfile(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty-step":    `{"profiles":{"p":{"steps":[{"name":"a","argv":[]}]}}}`,
		"bad-on-fail":   `{"profiles":{"p":{"steps":[{"name":"a","argv":["true"],"on_fail":"shrug"}]}}}`,
		"no-downgrade":  `{"profiles":{"p":{"deploy":["true"],"migrate":{"argv":["true"],"reversible":true}}}}`,
		"health-no-url": `{"profiles":{"p":{"deploy":["true"],"health":[{"name":"api"}]}}}`,
		"unknown-field": `{"profiles":{"p":{"deploy":["true"],"shell":"sh -c ls"}}}`,
		"no-profiles":   `{"profiles":{}}`,
	} {
		path := filepath.Join(dir, name+".json")
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
