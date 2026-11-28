package consolelog

import (
	"encoding/json"
	"strings"
	"testing"
)

const console = "Started by user Jane\n" +
	"[2026-03-01T10:00:00.000Z] [Pipeline] Start of Pipeline\n" +
	"[2026-03-01T10:00:00.100Z] [Pipeline] node\n" +
	"[2026-03-01T10:00:00.200Z] [Pipeline] {\n" +
	"[2026-03-01T10:00:01.000Z] [Pipeline] stage\n" +
	"[2026-03-01T10:00:01.000Z] [Pipeline] { (Build)\n" +
	"[2026-03-01T10:00:02.000Z] [Pipeline] sh\n" +
	"[2026-03-01T10:00:02.100Z] + go build ./...\n" +
	"[2026-03-01T10:00:30.000Z] \x1b[8mha:////4Hidden+Note==\x1b[0mbuild ok\n" +
	"[2026-03-01T10:00:31.000Z] [Pipeline] }\n" +
	"[2026-03-01T10:00:31.000Z] [Pipeline] // stage\n" +
	"[2026-03-01T10:00:32.000Z] [Pipeline] stage\n" +
	"[2026-03-01T10:00:32.000Z] [Pipeline] { (Test)\n" +
	"[2026-03-01T10:00:32.000Z] [Pipeline] parallel\n" +
	"[2026-03-01T10:00:32.000Z] [Pipeline] { (Branch: unit)\n" +
	"[2026-03-01T10:00:33.000Z] + go test ./...\n" +
	"[2026-03-01T10:00:40.000Z] go: downloading example.com/lib v1.2.3\n" +
	"[2026-03-01T10:00:41.000Z] dial tcp 10.0.0.5:443: connect: connection refused\n" +
	"[2026-03-01T10:00:50.000Z] --- FAIL: TestCheckout (0.01s)\n" +
	"[2026-03-01T10:00:50.100Z]     cart_test.go:42: expected 3, got 2\n" +
	"[2026-03-01T10:00:51.000Z] \x1b[31mFAIL\x1b[0m\texample.com/shop/cart\t0.5s\n" +
	"[2026-03-01T10:00:52.000Z] ERROR: script returned exit code 1\n" +
	"[2026-03-01T10:00:52.000Z] [Pipeline] }\n" +
	"[2026-03-01T10:00:52.000Z] [Pipeline] // parallel\n" +
	"[2026-03-01T10:00:52.000Z] [Pipeline] }\n" +
	"[2026-03-01T10:00:52.000Z] [Pipeline] // stage\n" +
	"[2026-03-01T10:00:53.000Z] [Pipeline] }\n" +
	"[2026-03-01T10:00:53.000Z] [Pipeline] End of Pipeline\n" +
	"Finished: FAILURE\n"

func TestParseStagesAndCleanup(t *testing.T) {
	p := Parse(console)
	if p.Result != "FAILURE" || p.Commands != 2 {
		t.Fatalf("result %q commands %d", p.Result, p.Commands)
	}
	for _, l := range p.Lines {
		if strings.HasPrefix(l.Text, "[Pipeline]") || strings.Contains(l.Text, "\x1b") || strings.Contains(l.Text, "ha:////") {
			t.Fatalf("not cleaned: %q", l.Text)
		}
	}
	names := []string{}
	for _, s := range p.Stages {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Build,Test,unit" {
		t.Fatalf("stages %v", names)
	}
	build, unit := p.Stages[0], p.Stages[2]
	if build.LastCommand != "go build ./..." || build.DurationSec != 30 || build.ErrorLines != 0 {
		t.Fatalf("build %+v", build)
	}
	if unit.ExitCode != "1" || unit.ErrorLines < 3 || unit.LastCommand != "go test ./..." {
		t.Fatalf("unit %+v", unit)
	}
	for _, l := range p.Lines {
		if l.Text == "build ok" && (l.Stage != "Build" || l.Time != "2026-03-01T10:00:30Z") {
			t.Fatalf("%+v", l)
		}
		if strings.HasPrefix(l.Text, "--- FAIL") && (l.Stage != "unit" || l.Command != "go test ./...") {
			t.Fatalf("%+v", l)
		}
		if l.Text == "Finished: FAILURE" && l.Stage != "" {
			t.Fatalf("stack must unwind: %+v", l)
		}
	}
}

func TestClockOnlyTimestamper(t *testing.T) {
	p := Parse("10:00:01 [Pipeline] { (Deploy)\n10:00:02 + kubectl apply\n10:00:03 Error: something broke\n")
	if len(p.Lines) != 2 || p.Lines[1].Clock != "10:00:03" || p.Lines[1].Stage != "Deploy" || p.Lines[1].Text != "Error: something broke" {
		t.Fatalf("%+v", p.Lines)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"ERROR: Could not find credentials matching deploy-key":        "credentials",
		"cp: cannot create regular file '/opt/x': Permission denied":   "permission",
		"curl: (6) Could not resolve host: repo.example.com":           "network",
		"fatal error: runtime: out of memory / OutOfMemoryError":       "resources",
		"fatal: could not read Username for 'https://git.example.com'": "git",
		"Error response from daemon: manifest unknown":                 "docker",
		"npm ERR! code ERESOLVE":                                       "dependency",
		"[ERROR] /src/App.java:[12,5] cannot find symbol":              "compile",
		"./cart.go:12:3: undefined: Foo":                               "compile",
		"groovy.lang.MissingPropertyException: No such property: env2": "pipeline_script",
		"--- FAIL: TestX (0.00s)":                                      "test",
		"Tests run: 10, Failures: 2, Errors: 0, Skipped: 0":            "test",
		"ERROR: Quality gate failed":                                   "quality",
		"Timeout has been exceeded":                                    "timeout",
		"[ERROR] BUILD FAILURE":                                        "build_tool",
		"Aborted by Jane":                                              "aborted",
		"ERROR: script returned exit code 2":                           "script_exit",
		"Error: unexpected token":                                      "error",
	}
	for in, want := range cases {
		if got, ok := Classify(in); !ok || got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
	for _, ok := range []string{"build ok", "Tests run: 10, Failures: 0, Errors: 0, Skipped: 1", "error handling improved in this release notes line"} {
		if c, hit := Classify(ok); hit {
			t.Errorf("false positive %q => %s", ok, c)
		}
	}
}

func TestAnalyzeErrorsLikelyCause(t *testing.T) {
	s := AnalyzeErrors(Parse(console))
	if s.FailedStage != "unit" || s.ExitCode != "1" {
		t.Fatalf("%+v", s)
	}
	c := s.LikelyCause
	if c == nil || c.Category != "network" || !strings.Contains(c.Text, "connection refused") || c.Command != "go test ./..." || c.Stage != "unit" {
		t.Fatalf("network error outranks the test failure and the exit code: %+v", c)
	}
	if len(c.Before) == 0 || !strings.Contains(c.Reason, "exited with code 1") {
		t.Fatalf("%+v", c)
	}
	if s.Groups[0].Category != "network" || s.Groups[len(s.Groups)-1].Category != "script_exit" {
		t.Fatalf("groups not ranked: %+v", s.Groups)
	}
	if s.Categories["test"] != 2 {
		t.Fatalf("%v", s.Categories)
	}
}

func TestErrorGroupingAndRedaction(t *testing.T) {
	p := Parse("[Pipeline] { (Deploy)\n" +
		"Error: login failed for user=deploy password=hunter2 attempt 1\n" +
		"Error: login failed for user=deploy password=hunter2 attempt 2\n" +
		"[Pipeline] }\n")
	s := AnalyzeErrors(p)
	b, _ := json.Marshal(s)
	if len(s.Groups) != 1 || s.Groups[0].Count != 2 || strings.Contains(string(b), "hunter2") {
		t.Fatalf("%s", b)
	}
	if s.FailedStage != "Deploy" || s.LikelyCause.Line != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestFailedTests(t *testing.T) {
	p := Parse(strings.Join([]string{
		"--- FAIL: TestCheckout (0.01s)", "--- FAIL: TestCheckout (0.01s)",
		"FAILED tests/test_cart.py::test_total - AssertionError",
		"[ERROR] com.example.CartTest.testTotal:42 expected:<3> but was:<2>",
		"[ERROR] Tests run: 12, Failures: 1, Errors: 0, Skipped: 2",
		"Tests run: 12, Failures: 1, Errors: 0, Skipped: 2",
		"[ERROR] Tests run: 12, Failures: 1, Errors: 0, Skipped: 2, Time elapsed: 1 s <<< FAILURE! - in com.example.CartTest",
		"CartTest > testTotal() FAILED",
		"  ✕ adds items (12 ms)",
		"FAIL src/cart.test.ts",
		"  Failed Shop.Tests.CartTests.Total [15 ms]",
	}, "\n"))
	tests, totals := FailedTests(p.Lines)
	got := map[string]string{}
	for _, f := range tests {
		got[f.Framework+":"+f.Name] = ""
	}
	for _, want := range []string{"go:TestCheckout", "pytest:tests/test_cart.py::test_total", "junit:com.example.CartTest.testTotal",
		"gradle:CartTest > testTotal()", "jest:adds items", "jest:src/cart.test.ts", "dotnet:Shop.Tests.CartTests.Total"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if len(tests) != 7 || totals == nil || totals.Run != 12 || totals.Failures != 1 || totals.Skipped != 2 {
		t.Fatalf("%d %+v", len(tests), totals)
	}
}

func TestStageCommandIsRedacted(t *testing.T) {
	p := Parse("[Pipeline] { (Build)\n+ curl -H 'Authorization: Bearer abcdefghijklmnop' https://x\n[Pipeline] }\n")
	if strings.Contains(p.Stages[0].LastCommand, "abcdefghijklmnop") {
		t.Fatalf("command not redacted: %q", p.Stages[0].LastCommand)
	}
}
