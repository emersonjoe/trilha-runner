// Command testreport turns a `go test -json` stream and a coverage profile
// into the markdown summary CI shows on every run: passed, failed and
// skipped (with why), the slowest tests, coverage per package — and fails
// when a test failed or coverage fell below the floor kept in the
// repository. The floor only rises: the report says when it can.
//
//	go test -json -coverprofile=coverage.out ./... > test.json
//	go run ./internal/testreport -json test.json -cover coverage.out -floor testdata/coverage-floor.txt
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

type event struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

type timing struct {
	Name    string
	Seconds float64
}

type summary struct {
	Passed, Failed, Skipped int
	Failures                []string
	Skips                   []string // "pkg.Test: reason"
	Slowest                 []timing
}

// summarize reads a `go test -json` stream. Only top-level tests count;
// subtests are part of their parent. A package that fails with no test
// failing (a build error) is a failure too.
func summarize(r io.Reader) (summary, error) {
	var s summary
	output := map[string][]string{}
	failedTests := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			return s, fmt.Errorf("testreport: %w", err)
		}
		name := shortPackage(e.Package) + "." + e.Test
		if e.Test == "" {
			if e.Action == "fail" && !failedTests[e.Package] {
				s.Failed++
				s.Failures = append(s.Failures, shortPackage(e.Package)+" (package)")
			}
			continue
		}
		if strings.Contains(e.Test, "/") {
			continue
		}
		switch e.Action {
		case "output":
			output[name] = append(output[name], e.Output)
		case "pass":
			s.Passed++
			s.Slowest = append(s.Slowest, timing{name, e.Elapsed})
		case "fail":
			s.Failed++
			failedTests[e.Package] = true
			s.Failures = append(s.Failures, name)
			s.Slowest = append(s.Slowest, timing{name, e.Elapsed})
		case "skip":
			s.Skipped++
			s.Skips = append(s.Skips, name+": "+skipReason(output[name]))
		}
	}
	sort.SliceStable(s.Slowest, func(i, j int) bool { return s.Slowest[i].Seconds > s.Slowest[j].Seconds })
	return s, scanner.Err()
}

// skipReason is the message t.Skip printed: `    file_test.go:33: Docker is unavailable`.
func skipReason(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "===") || strings.HasPrefix(line, "---") || line == "" {
			continue
		}
		if _, reason, ok := strings.Cut(line, ": "); ok && strings.Contains(line, "_test.go:") {
			return reason
		}
		return line
	}
	return "no reason given"
}

type coverage struct {
	Statements, Covered int
}

func (c coverage) Percent() float64 {
	if c.Statements == 0 {
		return 0
	}
	return 100 * float64(c.Covered) / float64(c.Statements)
}

// coverageByPackage reads a coverage profile. A block listed more than
// once counts once, covered if any listing covered it.
func coverageByPackage(r io.Reader) (map[string]coverage, coverage, error) {
	type block struct {
		pkg        string
		statements int
		covered    bool
	}
	blocks := map[string]*block{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, coverage{}, fmt.Errorf("testreport: coverage line %q", line)
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		file, _, ok := strings.Cut(fields[0], ":")
		if err1 != nil || err2 != nil || !ok {
			return nil, coverage{}, fmt.Errorf("testreport: coverage line %q", line)
		}
		if b, seen := blocks[fields[0]]; seen {
			b.covered = b.covered || count > 0
			continue
		}
		blocks[fields[0]] = &block{pkg: path.Dir(file), statements: statements, covered: count > 0}
	}
	if err := scanner.Err(); err != nil {
		return nil, coverage{}, err
	}
	byPackage := map[string]coverage{}
	var total coverage
	for _, b := range blocks {
		c := byPackage[b.pkg]
		c.Statements += b.statements
		total.Statements += b.statements
		if b.covered {
			c.Covered += b.statements
			total.Covered += b.statements
		}
		byPackage[b.pkg] = c
	}
	return byPackage, total, nil
}

// readFloor reads the first number in the floor file; # starts a comment.
func readFloor(r io.Reader) (float64, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		if line = strings.TrimSpace(line); line != "" {
			return strconv.ParseFloat(line, 64)
		}
	}
	return 0, fmt.Errorf("testreport: the floor file has no number")
}

// render answers the markdown and whether the run is acceptable.
func render(s summary, byPackage map[string]coverage, total coverage, floor float64, top int) (string, bool) {
	var b strings.Builder
	ok := s.Failed == 0 && total.Percent() >= floor
	mark := "✅"
	if !ok {
		mark = "❌"
	}
	fmt.Fprintf(&b, "## %s Test report\n\n", mark)
	fmt.Fprintf(&b, "| Passed | Failed | Skipped | Coverage | Floor |\n|---:|---:|---:|---:|---:|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %.1f%% | %.1f%% |\n\n", s.Passed, s.Failed, s.Skipped, total.Percent(), floor)
	if len(s.Failures) > 0 {
		b.WriteString("### Failed\n\n")
		for _, name := range s.Failures {
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
		b.WriteString("\n")
	}
	if total.Percent() < floor {
		fmt.Fprintf(&b, "**Coverage %.1f%% is below the floor %.1f%%** (`testdata/coverage-floor.txt`). Add the tests the change is missing; the floor is not lowered to fit.\n\n", total.Percent(), floor)
	} else if total.Percent() >= floor+1 {
		fmt.Fprintf(&b, "Coverage is %.1f points above the floor: raise `testdata/coverage-floor.txt` to %.1f.\n\n", total.Percent()-floor, float64(int(total.Percent()*10))/10)
	}
	if len(s.Skips) > 0 {
		b.WriteString("### Skipped\n\n")
		for _, skip := range s.Skips {
			fmt.Fprintf(&b, "- %s\n", skip)
		}
		b.WriteString("\n")
	}
	if len(s.Slowest) > 0 {
		b.WriteString("### Slowest\n\n| Test | Seconds |\n|---|---:|\n")
		for _, t := range s.Slowest[:min(top, len(s.Slowest))] {
			fmt.Fprintf(&b, "| `%s` | %.2f |\n", t.Name, t.Seconds)
		}
		b.WriteString("\n")
	}
	if len(byPackage) > 0 {
		b.WriteString("### Coverage by package\n\n| Package | Statements | Coverage |\n|---|---:|---:|\n")
		var names []string
		for name := range byPackage {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			c := byPackage[name]
			fmt.Fprintf(&b, "| `%s` | %d | %.1f%% |\n", shortPackage(name), c.Statements, c.Percent())
		}
	}
	return b.String(), ok
}

const module = "github.com/emersonjoe/trilha-runner"

func shortPackage(name string) string {
	if name == module {
		return "."
	}
	return strings.TrimPrefix(name, module+"/")
}

func main() {
	jsonPath := flag.String("json", "test.json", "output of go test -json")
	coverPath := flag.String("cover", "coverage.out", "coverage profile")
	floorPath := flag.String("floor", "testdata/coverage-floor.txt", "file holding the minimum total coverage")
	top := flag.Int("top", 10, "how many of the slowest tests to list")
	flag.Parse()
	if err := run(*jsonPath, *coverPath, *floorPath, *top, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(jsonPath, coverPath, floorPath string, top int, out io.Writer) error {
	open := func(name string) (*os.File, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, fmt.Errorf("testreport: %w", err)
		}
		return f, nil
	}
	events, err := open(jsonPath)
	if err != nil {
		return err
	}
	defer events.Close()
	s, err := summarize(events)
	if err != nil {
		return err
	}
	profile, err := open(coverPath)
	if err != nil {
		return err
	}
	defer profile.Close()
	byPackage, total, err := coverageByPackage(profile)
	if err != nil {
		return err
	}
	floorFile, err := open(floorPath)
	if err != nil {
		return err
	}
	defer floorFile.Close()
	floor, err := readFloor(floorFile)
	if err != nil {
		return err
	}
	report, ok := render(s, byPackage, total, floor, top)
	fmt.Fprint(out, report)
	if !ok {
		return fmt.Errorf("testreport: %d failed, coverage %.1f%% (floor %.1f%%)", s.Failed, total.Percent(), floor)
	}
	return nil
}
