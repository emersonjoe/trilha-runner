package catalog

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const root = "../.."

// A case is one **Jn.m …** entry of a catalog.
type testCase struct {
	ID     string
	Tests  []string
	Fields map[string]bool
}

var (
	reCase     = regexp.MustCompile(`^\*\*(J\d+\.\d+)\b`)
	reTestName = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	reField    = regexp.MustCompile(`^- ([^:]+):`)
	reTestFunc = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(t \*testing\.T\)`)
)

// Field names per locale: resources, preconditions, expected.
var required = map[string][3]string{
	"docs/journeys.md":       {"Resources", "Preconditions", "Expected"},
	"docs/journeys.pt-BR.md": {"Recursos", "Pré-condições", "Esperado"},
}

func parse(t *testing.T, name string) []testCase {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	var cases []testCase
	for _, line := range strings.Split(string(content), "\n") {
		if m := reCase.FindStringSubmatch(line); m != nil {
			cases = append(cases, testCase{ID: m[1], Fields: map[string]bool{}})
		} else if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "Open question") || strings.HasPrefix(line, "Questão em aberto") {
			// A journey heading or a note closes the case above it.
			cases = append(cases, testCase{})
		}
		if len(cases) == 0 || cases[len(cases)-1].ID == "" {
			continue
		}
		current := &cases[len(cases)-1]
		for _, m := range reTestName.FindAllStringSubmatch(line, -1) {
			current.Tests = append(current.Tests, m[1])
		}
		if m := reField.FindStringSubmatch(line); m != nil {
			current.Fields[m[1]] = true
		}
	}
	var out []testCase
	for _, c := range cases {
		if c.ID != "" {
			sort.Strings(c.Tests)
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: no cases", name)
	}
	return out
}

// testFuncs answers every top-level test function in the repository, by
// the file that declares it.
func testFuncs(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "artifacts" || entry.Name() == "bin") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range reTestFunc.FindAllStringSubmatch(string(content), -1) {
			found[m[1]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestCatalogReferencesExistingTests(t *testing.T) {
	funcs := testFuncs(t)
	for name := range required {
		for _, c := range parse(t, name) {
			for _, test := range c.Tests {
				if _, ok := funcs[test]; !ok {
					t.Errorf("%s %s names %s, which no *_test.go declares", name, c.ID, test)
				}
			}
		}
	}
}

func TestEveryTestBelongsToACase(t *testing.T) {
	named := map[string]bool{}
	for _, c := range parse(t, "docs/journeys.md") {
		for _, test := range c.Tests {
			named[test] = true
		}
	}
	for test, path := range testFuncs(t) {
		if !named[test] {
			t.Errorf("%s (%s) belongs to no case in docs/journeys.md", test, path)
		}
	}
}

func TestCatalogLocalesAgree(t *testing.T) {
	en, pt := parse(t, "docs/journeys.md"), parse(t, "docs/journeys.pt-BR.md")
	ids := func(cases []testCase) []string {
		var out []string
		for _, c := range cases {
			out = append(out, c.ID)
		}
		return out
	}
	if a, b := strings.Join(ids(en), " "), strings.Join(ids(pt), " "); a != b {
		t.Fatalf("case ids differ:\n en: %s\n pt: %s", a, b)
	}
	for i := range en {
		if a, b := strings.Join(en[i].Tests, " "), strings.Join(pt[i].Tests, " "); a != b {
			t.Errorf("%s names different tests:\n en: %s\n pt: %s", en[i].ID, a, b)
		}
	}
}

func TestEveryCaseIsComplete(t *testing.T) {
	for name, fields := range required {
		for _, c := range parse(t, name) {
			if len(c.Tests) == 0 {
				t.Errorf("%s %s names no test", name, c.ID)
			}
			for _, field := range fields {
				if !c.Fields[field] {
					t.Errorf("%s %s has no %q line", name, c.ID, field)
				}
			}
		}
	}
}

var (
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*`)
	reURL    = regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)
	reTokens = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	}
)

func syntheticEmail(address string) bool {
	switch address {
	case "t@t", "git@github.com": // a placeholder, and the form of an SSH remote
		return true
	}
	domain := address[strings.LastIndex(address, "@")+1:]
	return syntheticHost(domain) || domain == "trilha.local"
}

// markedFake is a credential-shaped string that says it is made up, the way
// AWS documents AKIAIOSFODNN7EXAMPLE.
func markedFake(token string) bool {
	return strings.Contains(token, "FAKE") || strings.Contains(token, "EXAMPLE") || strings.Contains(token, "SECRET")
}

// syntheticHost is a host nobody owns: an example domain, loopback, or a
// reserved local name.
func syntheticHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".test") {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "example" {
			return true
		}
	}
	return false
}

// Test data is synthetic: no real address, token or host in a fixture.
// Hosts are checked only in tests — a schema id or the deploy example's
// own control plane is a name, not a call.
func TestFixturesAreSynthetic(t *testing.T) {
	var files []string
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "artifacts" || entry.Name() == "bin") {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		if !entry.IsDir() && (strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "testdata"+string(filepath.Separator)) || (strings.HasPrefix(rel, "deploy"+string(filepath.Separator)) && strings.HasSuffix(path, ".example"))) {
			files = append(files, path)
		}
		return nil
	})
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	self, _ := filepath.Abs("catalog_test.go")
	for _, path := range files {
		if abs, _ := filepath.Abs(path); abs == self {
			continue // the patterns above
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, address := range reEmail.FindAllString(text, -1) {
			if !syntheticEmail(address) {
				t.Errorf("%s: e-mail %q is not synthetic (use t@t or …@example.com)", path, address)
			}
		}
		for _, re := range reTokens {
			for _, token := range re.FindAllString(text, -1) {
				if !markedFake(token) {
					t.Errorf("%s: %q looks like a real credential (mark a fake one with FAKE, EXAMPLE or SECRET)", path, token[:min(len(token), 8)]+"…")
				}
			}
		}
		if strings.HasSuffix(path, "_test.go") {
			for _, m := range reURL.FindAllStringSubmatch(text, -1) {
				if !syntheticHost(m[1]) {
					t.Errorf("%s: host %q is real (use a name under example)", path, m[1])
				}
			}
		}
	}
}

// The guards themselves: a parse that finds nothing would pass every check.
func TestParseReadsCasesTestsAndFields(t *testing.T) {
	cases := parse(t, "docs/journeys.md")
	byID := map[string]testCase{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	happy, ok := byID["J1.1"]
	if !ok || !strings.Contains(strings.Join(happy.Tests, " "), "TestRunEcho") || !happy.Fields["Expected"] {
		t.Fatalf("J1.1 = %+v", happy)
	}
	if !syntheticHost("gateway.example.br") || syntheticHost("github.com") || !syntheticEmail("t@t") || syntheticEmail("someone@gmail.com") ||
		!markedFake("sk-ant-oat-SECRET-0123456789") || markedFake("ghp_0123456789abcdefghij0123") {
		t.Fatal("host, e-mail or fake-token rules")
	}
}
