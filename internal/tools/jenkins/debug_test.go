package jenkins

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

func decode[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}

	return v
}

const runJSON = `{"id":"42","status":"FAILED","durationMillis":125300,"stages":[
  {"id":"6","name":"Checkout","status":"SUCCESS","durationMillis":2000},
  {"id":"12","name":"Build","status":"SUCCESS","durationMillis":60000},
  {"id":"20","name":"Test","status":"FAILED","durationMillis":50000,
   "error":{"message":"script returned exit code 2","type":"hudson.AbortException"},
   "stageFlowNodes":[
     {"id":"22","name":"Shell Script","status":"SUCCESS","parameterDescription":"go vet ./..."},
     {"id":"25","name":"Shell Script","status":"FAILED","parameterDescription":"go test ./...","error":{"message":"script returned exit code 2"}}]},
  {"id":"30","name":"Deploy","status":"NOT_EXECUTED","durationMillis":0}]}`

func debugAPI(t *testing.T) *fakeAPI {
	run := decode[client.PipelineRun](t, runJSON)
	node := decode[client.Stage](t, `{"id":"20","name":"Test","status":"FAILED","execNode":"agent-7","durationMillis":50000,
	  "stageFlowNodes":[{"id":"22","name":"Shell Script","status":"SUCCESS","parameterDescription":"go vet ./...","durationMillis":3000},
	  {"id":"25","name":"Shell Script","status":"FAILED","parameterDescription":"go test ./...","durationMillis":47000,"error":{"message":"script returned exit code 2"}}]}`)

	return &fakeAPI{
		runs:     map[int]*client.PipelineRun{42: &run},
		nodes:    map[string]*client.Stage{"20": &node},
		nodeLogs: map[string]*client.NodeLog{"25": {NodeID: "25", HasMore: true, Text: "--- FAIL: TestCheckout (0.01s)\n    cart_test.go:42: expected 3, got 2\nFAIL\n"}},
	}
}

func TestFailedStages(t *testing.T) {
	var out failedStagesOutput
	call(t, session(t, debugAPI(t)), "jenkins_get_failed_stages", map[string]any{"job": "svc/main", "build": "lastFailedBuild"}, &out)
	if out.Build != 42 || out.FirstFailed != "Test" || len(out.FailedStages) != 1 || len(out.Stages) != 4 {
		t.Fatalf("%+v", out)
	}
	if f := out.FailedStages[0]; f.ID != "20" || f.Error != "script returned exit code 2" || f.DurationSeconds != 50 {
		t.Fatalf("%+v", f)
	}
	if out.DurationSeconds != 125.3 {
		t.Fatalf("duration %v", out.DurationSeconds)
	}
}

func TestFailedStagesOutsideStage(t *testing.T) {
	api := debugAPI(t)
	r := decode[client.PipelineRun](t, `{"id":"42","status":"FAILED","stages":[{"id":"6","name":"Build","status":"SUCCESS"}]}`)
	api.runs[42] = &r
	var out failedStagesOutput
	call(t, session(t, api), "jenkins_get_failed_stages", map[string]any{"job": "svc"}, &out)
	if len(out.FailedStages) != 0 || !strings.Contains(strings.Join(out.Notes, " "), "outside any stage") {
		t.Fatalf("%+v", out)
	}
}

func TestStageDetails(t *testing.T) {
	cs := session(t, debugAPI(t))
	var out stageOutput
	call(t, cs, "jenkins_get_stage_details", map[string]any{"job": "svc", "stage": "test"}, &out)
	if out.StageID != "20" || out.Agent != "agent-7" || len(out.Steps) != 2 || out.FailedSteps != 1 || out.Steps[1].Argument != "go test ./..." {
		t.Fatalf("%+v", out)
	}
	call(t, cs, "jenkins_get_stage_details", map[string]any{"job": "svc", "stage": "20"}, &out)
	if out.Stage != "Test" {
		t.Fatalf("by id: %+v", out)
	}
	res := call(t, cs, "jenkins_get_stage_details", map[string]any{"job": "svc", "stage": "Lint"}, nil)
	if c := code(t, res); c != toolkit.CodeNotFound || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "Checkout, Build, Test, Deploy") {
		t.Fatalf("%s", c)
	}
}

func TestFailedStepLogs(t *testing.T) {
	var out stepLogsOutput
	call(t, session(t, debugAPI(t)), "jenkins_get_failed_step_logs", map[string]any{"job": "svc"}, &out)
	if len(out.Steps) != 1 {
		t.Fatalf("%+v", out)
	}
	s := out.Steps[0]
	if s.Stage != "Test" || s.StepID != "25" || s.Argument != "go test ./..." || !strings.Contains(s.Log, "expected 3, got 2") || !s.Truncated {
		t.Fatalf("%+v", s)
	}
	if !strings.Contains(strings.Join(out.Notes, " "), "10 KB") {
		t.Fatalf("truncation note missing: %v", out.Notes)
	}
}

func TestFailedStepLogsFallsBackToNodeDescribe(t *testing.T) {
	api := debugAPI(t)
	r := decode[client.PipelineRun](t, `{"id":"42","status":"FAILED","stages":[{"id":"20","name":"Test","status":"FAILED"}]}`)
	api.runs[42] = &r // older plugin: no stageFlowNodes in describe
	var out stepLogsOutput
	call(t, session(t, api), "jenkins_get_failed_step_logs", map[string]any{"job": "svc"}, &out)
	if len(out.Steps) != 1 || out.Steps[0].StepID != "25" {
		t.Fatalf("%+v", out)
	}
}

func TestNotAPipeline(t *testing.T) {
	api := &fakeAPI{runs: map[int]*client.PipelineRun{}}
	res := call(t, session(t, api), "jenkins_get_failed_stages", map[string]any{"job": "freestyle"}, nil)
	if c := code(t, res); c != toolkit.CodeNotFound {
		t.Fatalf("%s", c)
	}
}

func testReport(t *testing.T) *client.TestReport {
	r := decode[client.TestReport](t, `{"duration":12.5,"failCount":3,"passCount":96,"skipCount":1,"suites":[
	  {"name":"cart","duration":3,"cases":[
	    {"className":"shop.CartTest","name":"TestCheckout","status":"REGRESSION","age":1,"failedSince":42,
	     "errorDetails":"expected 3, got 2","errorStackTrace":"l1\nl2\nl3\nl4"},
	    {"className":"shop.CartTest","name":"TestEmpty","status":"PASSED"},
	    {"className":"shop.CartTest","name":"TestOld","status":"FAILED","age":7,"failedSince":36,"errorDetails":"timeout"}]},
	  {"name":"auth","duration":1,"cases":[
	    {"className":"shop.AuthTest","name":"TestLogin","status":"FAILED","age":3,"errorDetails":"401"},
	    {"className":"shop.AuthTest","name":"TestLogout","status":"FIXED"},
	    {"className":"shop.AuthTest","name":"TestSkip","status":"SKIPPED"}]}]}`)

	return &r
}

func TestTestResultsAndFailures(t *testing.T) {
	api := &fakeAPI{reports: map[int]*client.TestReport{42: testReport(t)}}
	cs := session(t, api)
	var sum testResultsOutput
	call(t, cs, "jenkins_get_test_results", map[string]any{"job": "svc"}, &sum)
	if sum.Total != 100 || sum.Failed != 3 || sum.NewFailures != 1 || sum.Fixed != 1 || len(sum.FailingSuites) != 2 || sum.FailingSuites[0].Name != "cart" {
		t.Fatalf("%+v", sum)
	}
	if api.trees[0] != client.TestTreeSummary {
		t.Fatalf("summary must use the small projection: %s", api.trees[0])
	}

	var f testFailuresOutput
	call(t, cs, "jenkins_get_test_failures", map[string]any{"job": "svc", "stackLines": 2}, &f)
	if f.TotalFailed != 3 || f.NewFailures != 1 || f.Failures[0].Name != "TestCheckout" || !f.Failures[0].New || f.Failures[1].Name != "TestLogin" {
		t.Fatalf("new first, then by age: %+v", f.Failures)
	}
	if f.Failures[0].StackTrace != "l1\nl2\n…" || f.Failures[0].Error != "expected 3, got 2" {
		t.Fatalf("%+v", f.Failures[0])
	}
	call(t, cs, "jenkins_get_test_failures", map[string]any{"job": "svc", "search": "auth", "limit": 1}, &f)
	if f.TotalFailed != 1 || f.Returned != 1 || f.Failures[0].ClassName != "shop.AuthTest" {
		t.Fatalf("%+v", f)
	}
	// No JUnit report.
	res := call(t, session(t, &fakeAPI{reports: map[int]*client.TestReport{}}), "jenkins_get_test_results", map[string]any{"job": "svc"}, nil)
	if c := code(t, res); c != toolkit.CodeNotFound {
		t.Fatalf("%s", c)
	}
}

func buildJSON(t *testing.T, raw string) *client.Build {
	b := decode[client.Build](t, raw)

	return &b
}

func TestBuildChanges(t *testing.T) {
	files := make([]string, 25)
	for i := range files {
		files[i] = "f" + string(rune('a'+i%26))
	}
	fj, _ := json.Marshal(files)
	b := buildJSON(t, `{"number":42,"culprits":[{"fullName":"Jane"}],
	  "actions":[{"lastBuiltRevision":{"SHA1":"abc","branch":[{"name":"origin/main"}]},"remoteUrls":["https://git.example.com/shop.git"]}],
	  "changeSets":[{"kind":"git","items":[{"commitId":"abc","msg":"fix cart","timestamp":1700000000000,"author":{"fullName":"Jane"},"affectedPaths":`+string(fj)+`}]}]}`)
	var out changesOutput
	call(t, session(t, &fakeAPI{builds: map[string]*client.Build{"42": b}}), "jenkins_get_build_changes", map[string]any{"job": "svc"}, &out)
	if out.Count != 1 || out.Git.Commit != "abc" || out.Culprits[0] != "Jane" {
		t.Fatalf("%+v", out)
	}
	if c := out.Commits[0]; len(c.Files) != maxFilesPerCommit || c.More != 5 || c.Time != "2023-11-14T22:13:20Z" || c.Build != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestCompareBuilds(t *testing.T) {
	good := buildJSON(t, `{"number":40,"result":"SUCCESS","duration":100000,"builtOn":"agent-1",
	  "actions":[{"parameters":[{"name":"ENV","value":"qa"},{"name":"DEBUG","value":false}]},{"lastBuiltRevision":{"SHA1":"aaa"}}]}`)
	mid := buildJSON(t, `{"number":41,"changeSets":[{"items":[{"commitId":"bbb","msg":"refactor","author":{"fullName":"Bob"}}]}]}`)
	bad := buildJSON(t, `{"number":42,"result":"FAILURE","duration":125000,"builtOn":"agent-7",
	  "actions":[{"parameters":[{"name":"ENV","value":"qa"},{"name":"DEBUG","value":true},{"name":"NEW","value":"x"}]},{"lastBuiltRevision":{"SHA1":"ccc"}}],
	  "changeSets":[{"items":[{"commitId":"ccc","msg":"fix cart","author":{"fullName":"Jane"}}]}]}`)
	goodRun := decode[client.PipelineRun](t, `{"stages":[{"name":"Build","status":"SUCCESS","durationMillis":50000},{"name":"Test","status":"SUCCESS","durationMillis":40000}]}`)
	badRun := decode[client.PipelineRun](t, runJSON)
	api := &fakeAPI{
		builds:   map[string]*client.Build{"40": good, "41": mid, "42": bad},
		resolved: map[string]int{"lastSuccessfulBuild": 40, "lastBuild": 42},
		runs:     map[int]*client.PipelineRun{40: &goodRun, 42: &badRun},
		reports:  map[int]*client.TestReport{42: {PassCount: 96, FailCount: 3}},
	}
	var out compareOutput
	call(t, session(t, api), "jenkins_compare_builds", map[string]any{"job": "svc"}, &out)
	if out.Base.Build != 40 || out.Target.Build != 42 || !out.ResultChanged || !out.GitChanged || !out.AgentChanged || out.DurationDelta != 25 {
		t.Fatalf("%+v", out)
	}
	pc, _ := json.Marshal(out.ParameterChanges)
	if string(pc) != `[{"name":"DEBUG","base":"false","target":"true"},{"name":"NEW","base":"","target":"x"}]` {
		t.Fatalf("params %s", pc)
	}
	if out.FirstDiffStage != "Test" || len(out.CommitsBetween) != 2 || out.CommitsBetween[0].Build != 41 || out.CommitsBetween[1].Commit != "ccc" {
		t.Fatalf("%+v %+v", out.FirstDiffStage, out.CommitsBetween)
	}
	for _, st := range out.Stages {
		if st.Stage == "Checkout" && st.OnlyIn != "target" {
			t.Fatalf("new stage must be marked: %+v", st)
		}
	}
	if out.Base.Tests != nil || out.Target.Tests == nil || out.Target.Tests.Failed != 3 {
		t.Fatalf("tests %+v %+v", out.Base.Tests, out.Target.Tests)
	}
}

func TestDebugToolsValidation(t *testing.T) {
	api := debugAPI(t)
	cs := session(t, api)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"jenkins_get_failed_stages", map[string]any{"job": "../x"}},
		{"jenkins_get_stage_details", map[string]any{"job": "svc", "stage": ""}},
		{"jenkins_get_failed_step_logs", map[string]any{"job": "svc", "maxSteps": 50}},
		{"jenkins_get_test_failures", map[string]any{"job": "svc", "limit": 1000}},
		{"jenkins_get_test_failures", map[string]any{"job": "svc", "stackLines": 500}},
		{"jenkins_compare_builds", map[string]any{"job": "svc", "base": "-3"}},
		{"jenkins_get_build_changes", map[string]any{"job": "svc", "build": "x/../y"}},
	} {
		if c := code(t, call(t, cs, tc.tool, tc.args, nil)); c != toolkit.CodeInvalidInput {
			t.Errorf("%s %v: %s", tc.tool, tc.args, c)
		}
	}
	if api.calls != 0 {
		t.Fatalf("API called %d times for invalid input", api.calls)
	}
}
