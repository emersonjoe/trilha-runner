// Package scripts holds the tests of the repository's shell scripts.
package scripts

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A synthetic key: the shape of a real one, valid nowhere.
const fakeKey = "sk-ant-api03-SYNTHETIC0000000000000000000000000000000000-FAKEaaaa"

// keyScript runs anthropic-key.sh against the file store in a temporary
// config directory and a fake API that accepts only fakeKey; it answers the
// runner and the path the key file goes to.
func keyScript(t *testing.T) (func(stdin string, args ...string) (string, error), string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("x-api-key") != fakeKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(api.Close)
	config := t.TempDir()
	keyFile := filepath.Join(config, "trilha", "anthropic-e2e.key")
	return func(stdin string, args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{"anthropic-key.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "ANTHROPIC_KEY_BACKEND=file", "XDG_CONFIG_HOME="+config, "ANTHROPIC_BASE_URL="+api.URL, "ANTHROPIC_API_KEY=")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}, keyFile
}

func TestAnthropicKeyStoresChecksRunsAndForgets(t *testing.T) {
	run, config := keyScript(t)

	out, err := run(fakeKey+"\n", "set")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if strings.Contains(out, fakeKey) || !strings.Contains(out, "sk-ant-…aaaa") || !strings.Contains(out, "the API accepts it") {
		t.Fatalf("set must print the key masked and checked:\n%s", out)
	}
	info, err := os.Stat(config)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key file must exist with mode 0600: %v %v", err, info)
	}

	out, err = run("", "status")
	if err != nil || strings.Contains(out, fakeKey) || !strings.Contains(out, "the API accepts it") {
		t.Fatalf("status: %v\n%s", err, out)
	}

	// exec hands the key to one process through its environment, nowhere else.
	out, err = run("", "exec", "--", "sh", "-c", `test "$ANTHROPIC_API_KEY" = "`+fakeKey+`" && echo injected`)
	if err != nil || strings.TrimSpace(out) != "injected" {
		t.Fatalf("exec did not inject the key: %v\n%s", err, out)
	}

	os.Chmod(config, 0o644)
	if out, err = run("", "exec", "--", "true"); err == nil || !strings.Contains(out, "0600") {
		t.Fatalf("a key file readable by others must be refused: %v\n%s", err, out)
	}
	os.Chmod(config, 0o600)

	if out, err = run("", "forget"); err != nil {
		t.Fatalf("forget: %v\n%s", err, out)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatal("forget left the key file behind")
	}
	if out, err = run("", "exec", "--", "true"); err == nil || !strings.Contains(out, "make claude-key") {
		t.Fatalf("exec without a key must say how to store one: %v\n%s", err, out)
	}
}

func TestAnthropicKeyRefusesWhatTheAPIRefusesAndWhatIsNotAKey(t *testing.T) {
	run, _ := keyScript(t)
	revoked := strings.Replace(fakeKey, "FAKEaaaa", "FAKEbbbb", 1)
	if out, err := run(revoked+"\n", "set"); err == nil || !strings.Contains(out, "401") || !strings.Contains(out, "nothing stored") {
		t.Fatalf("a key the API refuses must not be stored: %v\n%s", err, out)
	}
	for _, bad := range []string{"", "hunter2", "sk-ant-short", "sk-ant-api03-FAKEAAAAAAAAAAAAAAAAAAAA\"; rm -rf ~"} {
		if out, err := run(bad+"\n", "set"); err == nil || strings.Contains(out, "stored sk-ant") {
			t.Fatalf("%q must be refused before it is stored: %v\n%s", bad, err, out)
		}
	}
	if out, err := run("", "status"); err == nil || !strings.Contains(out, "no key stored") {
		t.Fatalf("nothing was stored, status must say so: %v\n%s", err, out)
	}
}

// The script never puts the key on a command line, where ps would show it:
// every use goes through stdin or the environment of the one exec'd process.
func TestAnthropicKeyNeverPassesTheKeyAsAnArgument(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".", "anthropic-key.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		for _, tool := range []string{"curl", "security", "secret-tool"} {
			if strings.HasPrefix(code, tool+" ") && strings.Contains(code, "$key") {
				t.Errorf("line %d passes the key to %s as an argument: %s", i+1, tool, code)
			}
		}
	}
}
