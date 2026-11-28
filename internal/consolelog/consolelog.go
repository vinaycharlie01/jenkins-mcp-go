// Package consolelog understands Jenkins console output: Pipeline stage
// markers, Timestamper prefixes, shell command echoes, hidden console notes,
// and CI-specific error lines (compile, test, dependency, docker, git,
// network, credentials, resources, ...). It is pure Go.
package consolelog

import (
	"regexp"
	"strings"
	"time"

	"github.com/vinaycharlie01/jenkins-mcp-go/internal/loganalysis"
)

// Line is one content line of the console (Pipeline marker lines excluded).
type Line struct {
	No      int    // 1-based line number within the scanned text
	Time    string // RFC3339 when the Timestamper prefix had a date
	Clock   string // HH:MM:SS when only a time was printed
	Stage   string // innermost Pipeline stage ("" outside stages)
	Command string // last "+ cmd" echoed before this line in the same stage
	Text    string // the line without timestamp, ANSI codes and console notes
	// Category is the CI error category ("" when the line is not an error).
	Category string
}

// Stage is one Pipeline stage (or parallel branch) seen in the console.
type Stage struct {
	Name        string `json:"name"`
	FirstLine   int    `json:"first_line"`
	LastLine    int    `json:"last_line"`
	Lines       int    `json:"lines"`
	Start       string `json:"start,omitempty"`
	End         string `json:"end,omitempty"`
	DurationSec int    `json:"duration_seconds,omitempty"`
	ErrorLines  int    `json:"error_lines"`
	ExitCode    string `json:"exit_code,omitempty"` // from "script returned exit code N"
	LastCommand string `json:"last_command,omitempty"`
	start, end  time.Time
}

// Parsed is a parsed console log.
type Parsed struct {
	Lines    []Line
	Stages   []Stage
	Result   string // from "Finished: X"
	Commands int
}

var (
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	noteRe     = regexp.MustCompile(`\x1b\[8mha:[^\x1b]*\x1b\[0m`)
	isoStampRe = regexp.MustCompile(`^\[?(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)\]?\s?`)
	clockRe    = regexp.MustCompile(`^\[?(\d{2}:\d{2}:\d{2})\]?\s`)
	openRe     = regexp.MustCompile(`^\[Pipeline\] \{(?: \((.*)\))?\s*$`)
	finishedRe = regexp.MustCompile(`^Finished: (SUCCESS|FAILURE|UNSTABLE|ABORTED|NOT_BUILT)\b`)
	exitRe     = regexp.MustCompile(`script returned exit code (\d+)`)
)

// Parse splits console text into content lines attributed to stages.
func Parse(text string) Parsed {
	var p Parsed
	var stack []string // stage names; "" for unnamed blocks (node, withEnv, ...)
	idx := map[string]int{}
	lastCmd := map[string]string{}

	cur := func() string {
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i] != "" {
				return stack[i]
			}
		}

		return ""
	}

	touch := func(name, stamp string) {
		if name == "" {
			return
		}
		if t, err := time.Parse(time.RFC3339Nano, normStamp(stamp)); err == nil {
			st := &p.Stages[idx[name]]
			if st.start.IsZero() {
				st.start = t
			}
			st.end = t
		}
	}
	rawLines := strings.Split(text, "\n")
	if n := len(rawLines); n > 0 && rawLines[n-1] == "" {
		rawLines = rawLines[:n-1] // trailing newline
	}
	for i, raw := range rawLines {
		no := i + 1
		s := strings.TrimRight(raw, "\r")
		s = noteRe.ReplaceAllString(s, "")
		s = ansiRe.ReplaceAllString(s, "")
		var stamp, clock string
		if m := isoStampRe.FindStringSubmatch(s); m != nil {
			stamp, s = m[1], s[len(m[0]):]
		} else if m := clockRe.FindStringSubmatch(s); m != nil {
			clock, s = m[1], s[len(m[0]):]
		}

		if strings.HasPrefix(s, "[Pipeline] ") {
			switch {
			case openRe.MatchString(s):
				name := strings.TrimPrefix(openRe.FindStringSubmatch(s)[1], "Branch: ")
				stack = append(stack, name)
				if name != "" {
					if _, ok := idx[name]; !ok {
						idx[name] = len(p.Stages)
						p.Stages = append(p.Stages, Stage{Name: name, FirstLine: no})
					}
					touch(name, stamp)
				}
			case strings.TrimSpace(s) == "[Pipeline] }":
				touch(cur(), stamp)
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}

			continue
		}
		if m := finishedRe.FindStringSubmatch(s); m != nil {
			p.Result = m[1]
		}
		stage := cur()
		if strings.HasPrefix(s, "+ ") {
			lastCmd[stage] = strings.TrimSpace(s[2:])
			p.Commands++
		}
		cat, _ := Classify(s)
		ln := Line{No: no, Time: normStamp(stamp), Clock: clock, Stage: stage, Command: lastCmd[stage], Text: s, Category: cat}
		p.Lines = append(p.Lines, ln)

		if stage != "" {
			st := &p.Stages[idx[stage]]
			st.LastLine = no
			st.Lines++
			st.LastCommand = loganalysis.Clip(lastCmd[stage], 300) // commands can carry secrets
			touch(stage, stamp)
			if m := exitRe.FindStringSubmatch(s); m != nil {
				st.ExitCode = m[1]
			}
			if cat != "" {
				st.ErrorLines++
			}
		}
	}
	for i := range p.Stages {
		st := &p.Stages[i]
		if !st.start.IsZero() {
			st.Start, st.End = st.start.Format(time.RFC3339), st.end.Format(time.RFC3339)
			st.DurationSec = int(st.end.Sub(st.start).Seconds())
		}
	}

	return p
}

func normStamp(s string) string {
	if s == "" {
		return ""
	}
	s = strings.Replace(s, " ", "T", 1)
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}

	return ""
}

// Error categories, most root-cause-like first (lower = more likely the cause).
var categoryRank = map[string]int{
	"credentials": 1, "permission": 2, "network": 3, "resources": 4, "git": 5, "docker": 6, "dependency": 7,
	"compile": 8, "pipeline_script": 9, "test": 10, "quality": 11, "timeout": 12, "build_tool": 13,
	"aborted": 14, "error": 15, "script_exit": 16,
}

// Rank returns the category's root-cause rank (lower is more specific).
func Rank(category string) int {
	if r, ok := categoryRank[category]; ok {
		return r
	}

	return 99
}

// Each rule has lower-case literal hints; its regex only runs when one of
// them occurs in the line. Most console lines contain no hint at all, so no
// regex runs for them.
var rules = []struct {
	category string
	hints    []string
	re       *regexp.Regexp
}{
	{"credentials", []string{"credential", "authentication", "invalid username"},
		regexp.MustCompile(`(?i)could not find credentials|CredentialNotFoundException|authentication (?:failed|required)|invalid username or password|bad credentials`)},
	{"permission", []string{"permission denied", "eacces", "403 forbidden", "access denied", "not authorized", "requested access"},
		regexp.MustCompile(`(?i)permission denied|\bEACCES\b|403 Forbidden|access denied|is not authorized to|requested access to the resource is denied`)},
	{"network", []string{"resolve host", "refused", "connection reset", "connection timed out", "name resolution", "tls handshake", "i/o timeout", "econnrefused", "etimedout", "enotfound", "no route to host", "503 service", "502 bad"},
		regexp.MustCompile(`(?i)could not resolve host|connection refused|connection reset|connection timed out|temporary failure in name resolution|TLS handshake timeout|i/o timeout|\bECONNREFUSED\b|\bETIMEDOUT\b|\bENOTFOUND\b|no route to host|503 Service Unavailable|502 Bad Gateway`)},
	{"resources", []string{"no space left", "outofmemory", "oomkilled", "allocate memory", "exit code 137", "heap out of memory", "killed"},
		regexp.MustCompile(`(?i)no space left on device|OutOfMemoryError|\bOOMKilled\b|cannot allocate memory|exit code 137|JavaScript heap out of memory|\bKilled$`)},
	{"git", []string{"fatal: ", "error cloning", "error fetching", "revision to build", "gitexception", "merge conflict"},
		regexp.MustCompile(`(?i)^fatal: |ERROR: Error (?:cloning|fetching)|Couldn't find any revision to build|hudson\.plugins\.git\.GitException|merge conflict`)},
	{"docker", []string{"from daemon", "failed to solve", "manifest unknown", "pull access denied", "toomanyrequests", "imagepullbackoff", "failed to push", "denied: "},
		regexp.MustCompile(`(?i)Error response from daemon|failed to solve|manifest unknown|pull access denied|toomanyrequests|ImagePullBackOff|failed to push|denied: `)},
	{"dependency", []string{"npm err!", "could not resolve", "could not find artifact", "modulenotfounderror", "no matching distribution", "unknown revision", "cannot find module", "failed to collect", "eresolve", "not found"},
		regexp.MustCompile(`(?i)npm ERR! (?:code E(?:RESOLVE|404|NOTFOUND)|404)|Could not resolve dependencies|Could not resolve all (?:files|dependencies)|Could not find artifact|ModuleNotFoundError|No matching distribution found|unknown revision|cannot find module|Failed to collect dependencies|\bERESOLVE\b|module .* not found`)},
	{"compile", []string{"compilation", "cannot find symbol", "error:", ".go:", "error ts", "error[e", "syntaxerror", "undefined: ", "error cs", ".java:["},
		regexp.MustCompile(`COMPILATION ERROR|cannot find symbol|error: \S+\.(?:java|kt|c|cpp|h)\b|\S+\.go:\d+:\d+: |\.(?:ts|tsx|js)\(\d+,\d+\): error TS\d+|error TS\d+:|error\[E\d+\]|SyntaxError|\bundefined: |error CS\d+|[Cc]ompilation failed|Compilation failure|[^\s]+\.java:\[\d+,\d+\]`)},
	{"pipeline_script", []string{"groovy.lang", "scriptsecurity", "scripts not permitted", "no such dsl", "workflowscript", "missingproperty", "missingmethod", "nosuchmethoderror"},
		regexp.MustCompile(`groovy\.lang\.\w+Exception|org\.jenkinsci\.plugins\.scriptsecurity|Scripts not permitted|No such DSL method|WorkflowScript: \d+:|MissingPropertyException|MissingMethodException|NoSuchMethodError`)},
	{"test", []string{"--- fail", "fail", "tests run", "failure", "assertionerror", "✕", "failing"},
		regexp.MustCompile(`--- FAIL: |^FAIL\s|Tests run: \d+, Failures: [1-9]|Tests run: \d+, Failures: \d+, Errors: [1-9]|There (?:was|were) \d+ failures?|^FAILED \S+::|\bAssertionError\b|Tests? failed|✕ |^\s*\d+ failing|> \S+ FAILED$|Failed \S+ \[`)},
	{"quality", []string{"quality gate", "lint", "problem", "golangci"},
		regexp.MustCompile(`(?i)quality gate (?:failed|status: error)|lint(?:ing)? (?:errors|failed)|\d+ problems? \(\d+ errors?|golangci-lint.*issues`)},
	{"timeout", []string{"timeout", "timed out", "deadline exceeded"},
		regexp.MustCompile(`(?i)timeout has been exceeded|timed out after|deadline exceeded|\btimed out\b`)},
	{"build_tool", []string{"build failure", "build failed", "make: ***", "execution failed", "command failed", "elifecycle", "completed with exit code"},
		regexp.MustCompile(`BUILD FAILURE|FAILURE: Build failed with an exception|make: \*\*\* |Execution failed for task|error Command failed with exit code|npm ERR! code ELIFECYCLE|Process completed with exit code [1-9]`)},
	{"aborted", []string{"aborted by", "interrupt signal", "flowinterrupted", "was aborted"},
		regexp.MustCompile(`Aborted by |Sending interrupt signal to process|FlowInterruptedException|Build was aborted`)},
	{"script_exit", []string{"script returned exit code"},
		regexp.MustCompile(`script returned exit code [1-9]`)},
	{"error", []string{"error", "fatal", "panic: "},
		regexp.MustCompile(`^\s*(?:\[ERROR\]|ERROR:|ERROR |Error: |error: |\[error\]|FATAL:|fatal error)|\bpanic: `)},
}

// Classify returns the CI error category of a console line, if any.
func Classify(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, r := range rules {
		hit := false
		for _, h := range r.hints {
			if strings.Contains(lower, h) {
				hit = true

				break
			}
		}
		if hit && r.re.MatchString(text) {
			return r.category, true
		}
	}

	return "", false
}

// failed-test extraction
var testRules = []struct {
	framework string
	re        *regexp.Regexp
}{
	{"go", regexp.MustCompile(`--- FAIL: (\S+)`)},
	{"pytest", regexp.MustCompile(`^FAILED (\S+::\S+)`)},
	{"junit", regexp.MustCompile(`^\[ERROR\]\s+([\w$]+(?:\.[\w$]+)+[.#]\w+)(?::\d+|\(|\s|$)`)},
	{"gradle", regexp.MustCompile(`^(\S+) > (.+) FAILED$`)},
	{"jest", regexp.MustCompile(`^\s*✕ (.+?)(?: \(\d+ ?m?s\))?$`)},
	{"jest", regexp.MustCompile(`^FAIL (\S+\.(?:test|spec)\.[jt]sx?)`)},
	{"dotnet", regexp.MustCompile(`^\s*Failed (\S+) \[`)},
}

var junitTotalsRe = regexp.MustCompile(`Tests run: (\d+), Failures: (\d+), Errors: (\d+), Skipped: (\d+)`)

// FailedTest is one failed test found in the console.
type FailedTest struct {
	Name      string `json:"name"`
	Framework string `json:"framework"`
	Stage     string `json:"stage,omitempty"`
	Line      int    `json:"line"`
}

// TestTotals are the last JUnit-style totals printed (Maven/Surefire).
type TestTotals struct {
	Run      int `json:"run"`
	Failures int `json:"failures"`
	Errors   int `json:"errors"`
	Skipped  int `json:"skipped"`
}

// FailedTests extracts failed test names (deduplicated) and the last totals line.
func FailedTests(lines []Line) ([]FailedTest, *TestTotals) {
	var out []FailedTest
	seen := map[string]bool{}
	var totals *TestTotals
	for _, l := range lines {
		if m := junitTotalsRe.FindStringSubmatch(l.Text); m != nil {
			totals = &TestTotals{Run: atoi(m[1]), Failures: atoi(m[2]), Errors: atoi(m[3]), Skipped: atoi(m[4])}
		}
		for _, r := range testRules {
			m := r.re.FindStringSubmatch(l.Text)
			if m == nil {
				continue
			}
			name := m[1]
			if r.framework == "gradle" {
				name = m[1] + " > " + m[2]
			}
			if k := r.framework + "\x00" + name; !seen[k] {
				seen[k] = true
				out = append(out, FailedTest{Name: name, Framework: r.framework, Stage: l.Stage, Line: l.No})
			}

			break
		}
	}

	return out, totals
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}

	return n
}
