package jenkins

import (
	"encoding/json"
	"strings"
	"testing"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

const buildConsole = "Started by user Jane\n" +
	"[2026-03-01T10:00:00.000Z] [Pipeline] { (Checkout)\n" +
	"[2026-03-01T10:00:01.000Z] + git fetch origin\n" +
	"[2026-03-01T10:00:05.000Z] [Pipeline] }\n" +
	"[2026-03-01T10:00:06.000Z] [Pipeline] { (Build)\n" +
	"[2026-03-01T10:00:06.500Z] + mvn -B package -Dtoken=s3cr3tvalue\n" +
	"[2026-03-01T10:00:10.000Z] [INFO] Building shop 1.0\n" +
	"[2026-03-01T10:01:00.000Z] [ERROR] Tests run: 12, Failures: 1, Errors: 0, Skipped: 2, Time elapsed: 1.2 s <<< FAILURE! - in com.example.CartTest\n" +
	"[2026-03-01T10:01:00.100Z] [ERROR] com.example.CartTest.testTotal:42 expected:<3> but was:<2>\n" +
	"[2026-03-01T10:01:00.200Z] java.lang.AssertionError: expected:<3> but was:<2>\n" +
	"[2026-03-01T10:01:00.300Z] \tat org.junit.Assert.fail(Assert.java:89)\n" +
	"[2026-03-01T10:01:00.400Z] \tat com.example.CartTest.testTotal(CartTest.java:42)\n" +
	"[2026-03-01T10:01:01.000Z] Tests run: 12, Failures: 1, Errors: 0, Skipped: 2\n" +
	"[2026-03-01T10:01:02.000Z] [ERROR] BUILD FAILURE\n" +
	"[2026-03-01T10:01:02.500Z] Authorization: Bearer abcdefghijklmnopqrstu\n" +
	"[2026-03-01T10:01:03.000Z] ERROR: script returned exit code 1\n" +
	"[2026-03-01T10:01:03.000Z] [Pipeline] }\n" +
	"[2026-03-01T10:01:04.000Z] groovy.lang.MissingPropertyException: No such property: deployEnv for class: WorkflowScript\n" +
	"Finished: FAILURE\n"

func analyzeAPI() *fakeAPI {
	return &fakeAPI{
		build:   &client.Build{Number: 77, Result: "FAILURE"},
		console: &client.Console{Text: buildConsole, Size: int64(len(buildConsole))},
	}
}

func runAnalyze(t *testing.T, api *fakeAPI, args map[string]any) analyzeBuildOutput {
	t.Helper()
	var out analyzeBuildOutput
	call(t, session(t, api), "jenkins_analyze_build_logs", args, &out)

	return out
}

func TestAnalyzeOverviewLikelyCause(t *testing.T) {
	api := analyzeAPI()
	out := runAnalyze(t, api, map[string]any{"job": "shop/main", "build": "lastFailedBuild"})
	if api.ref != "77" || api.start != -1 || api.maxBytes != defaultScanBytesAnalyze {
		t.Fatalf("console must be read from the pinned build's tail: ref=%s start=%d max=%d", api.ref, api.start, api.maxBytes)
	}
	if out.Build != 77 || out.Result != "FAILURE" || out.Summary.FailedStage != "Build" || out.Summary.Stages != 2 || out.Summary.PartialLog {
		t.Fatalf("%+v", out.Summary)
	}
	c := out.Results.LikelyCause
	if c == nil || c.Category != "test" || c.Stage != "Build" || !strings.HasPrefix(c.Command, "mvn -B package") {
		t.Fatalf("likely cause %+v", c)
	}
	if len(out.Results.Stages) != 2 || out.TimeRange.From != "2026-03-01T10:00:01Z" {
		t.Fatalf("%+v %+v", out.Results.Stages, out.TimeRange)
	}
}

func TestAnalyzeErrorsStagesTests(t *testing.T) {
	api := analyzeAPI()
	out := runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "errors"})
	e := out.Results.Errors
	if e == nil || e.Categories["test"] < 2 || e.Categories["build_tool"] != 1 || e.Categories["pipeline_script"] != 1 || e.ExitCode != "1" {
		t.Fatalf("%+v", e)
	}
	if out.TotalMatches != len(e.Groups) || e.Groups[0].Category != "pipeline_script" {
		t.Fatalf("groups must be ranked by specificity: %+v", e.Groups)
	}

	out = runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "stages"})
	if len(out.Results.Stages) != 2 || out.Results.Stages[1].Name != "Build" || out.Results.Stages[1].ExitCode != "1" || out.Results.Stages[1].DurationSec != 57 {
		t.Fatalf("%+v", out.Results.Stages)
	}

	out = runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "tests"})
	tr := out.Results.Tests
	if tr == nil || tr.Totals.Failures != 1 || len(tr.Failed) != 1 || tr.Failed[0].Name != "com.example.CartTest.testTotal" || tr.Failed[0].Stage != "Build" {
		t.Fatalf("%+v", tr)
	}
}

func TestAnalyzeExceptionsSearchStageFilter(t *testing.T) {
	api := analyzeAPI()
	out := runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "exceptions"})
	types := map[string]int{}
	for _, g := range out.Results.Exceptions {
		types[g.Type] = g.Count
	}
	if types["groovy.lang.MissingPropertyException"] != 1 {
		t.Fatalf("%v", types)
	}

	out = runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "search", "search": "tests run", "context_lines": 1, "max_results": 1})
	if out.TotalMatches < 2 || len(out.Results.Matches) != 1 || out.NextOffset == nil || !out.Truncated || out.Results.Matches[0].LineNo == 0 {
		t.Fatalf("%+v", out)
	}

	out = runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "errors", "stage": "checkout"})
	if out.Stage != "Checkout" || out.Results.Errors.ErrorLines != 0 || out.Summary.Lines != 1 {
		t.Fatalf("stage filter: %+v", out)
	}
	res := call(t, session(t, api), "jenkins_analyze_build_logs", map[string]any{"job": "shop/main", "stage": "Deploy"}, nil)
	if c := code(t, res); c != toolkit.CodeNotFound {
		t.Fatalf("unknown stage: %s", c)
	}
}

func TestAnalyzeAllSampleNoSecrets(t *testing.T) {
	api := analyzeAPI()
	out := runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "all"})
	r := out.Results
	if r.LikelyCause == nil || r.Errors == nil || r.Stages == nil || r.Tests == nil || r.Exceptions == nil || r.Patterns == nil || r.Matches != nil {
		t.Fatalf("%+v", r)
	}
	for _, mode := range []string{"overview", "errors", "stages", "tests", "exceptions", "patterns", "sample", "all"} {
		res := call(t, session(t, analyzeAPI()), "jenkins_analyze_build_logs", map[string]any{"job": "shop/main", "analysis_type": mode, "search": "token", "max_results": 200}, nil)
		b, _ := json.Marshal(res)
		for _, secret := range []string{"s3cr3tvalue", "abcdefghijklmnopqrstu"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s leaked %q", mode, secret)
			}
		}
	}
	out = runAnalyze(t, api, map[string]any{"job": "shop/main", "analysis_type": "sample", "max_results": 3})
	if len(out.Results.Matches) != 3 || out.Results.Matches[0].LineNo < out.Results.Matches[1].LineNo {
		t.Fatalf("sample newest first: %+v", out.Results.Matches)
	}
}

func TestAnalyzePartialAndRunning(t *testing.T) {
	api := analyzeAPI()
	api.build = &client.Build{Number: 78, Building: true}
	api.console = &client.Console{Text: "[2026-03-01T10:00:00Z] + make\n", Size: 50 << 20, Start: 42 << 20, MoreData: true}
	out := runAnalyze(t, api, map[string]any{"job": "shop/main", "scan_bytes": 1 << 20})
	notes := strings.Join(out.Summary.Notes, " ")
	if out.Result != "BUILDING" || !out.Summary.PartialLog || !strings.Contains(notes, "earlier stages may be missing") || !strings.Contains(notes, "still running") {
		t.Fatalf("%+v", out.Summary)
	}
	if api.maxBytes != 1<<20 {
		t.Fatalf("scan_bytes %d", api.maxBytes)
	}
}

func TestAnalyzeBuildLogsValidation(t *testing.T) {
	api := analyzeAPI()
	cs := session(t, api)
	for _, args := range []map[string]any{
		{"job": ""}, {"job": "../x"}, {"job": "shop", "build": "nope"},
		{"job": "shop", "analysis_type": "everything"},
		{"job": "shop", "analysis_type": "search"},
		{"job": "shop", "level": "loud"},
		{"job": "shop", "max_results": 1000},
		{"job": "shop", "scan_bytes": 64 << 20},
		{"job": "shop", "context_lines": 9},
		{"job": "shop", "offset": -1},
	} {
		if c := code(t, call(t, cs, "jenkins_analyze_build_logs", args, nil)); c != toolkit.CodeInvalidInput {
			t.Errorf("%v: %s", args, c)
		}
	}
	if api.calls != 0 {
		t.Fatalf("API called %d times for invalid input", api.calls)
	}
}
