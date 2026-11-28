package jenkins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

type fakeAPI struct {
	jobs     []client.JobSummary
	job      *client.Job
	build    *client.Build
	history  []client.BuildSummary
	console  *client.Console
	queue    []client.QueueItem
	who      *client.WhoAmI
	files    map[string]string
	builds   map[string]*client.Build
	resolved map[string]int
	runs     map[int]*client.PipelineRun
	nodes    map[string]*client.Stage
	nodeLogs map[string]*client.NodeLog
	reports  map[int]*client.TestReport
	trees    []string
	refs     []string
	err      error
	calls    int
	path     []string
	ref      string
	start    int64
	maxBytes int
	limit    int
}

func (f *fakeAPI) BaseFolder() string { return "TeamA" }
func (f *fakeAPI) RelPath(n string) string {
	return strings.TrimPrefix(n, "TeamA/")
}

func (f *fakeAPI) WhoAmI(context.Context) (*client.WhoAmI, error) {
	f.calls++

	return f.who, f.err
}

func (f *fakeAPI) ListJobs(_ context.Context, p []string) ([]client.JobSummary, error) {
	f.calls++
	f.path = p

	return f.jobs, f.err
}

func (f *fakeAPI) GetJob(_ context.Context, p []string) (*client.Job, error) {
	f.calls++
	f.path = p

	return f.job, f.err
}

func (f *fakeAPI) GetBuild(_ context.Context, p []string, ref string) (*client.Build, error) {
	f.calls++
	f.path, f.ref = p, ref
	if b, ok := f.builds[ref]; ok {
		return b, nil
	}
	if f.builds != nil && f.build == nil {
		return nil, &client.APIError{Status: 404, Reason: "x"}
	}

	return f.build, f.err
}

func (f *fakeAPI) ResolveBuild(_ context.Context, p []string, ref string) (int, string, bool, error) {
	f.calls++
	f.path = p
	if f.err != nil {
		return 0, "", false, f.err
	}
	if n, ok := f.resolved[ref]; ok {
		return n, "FAILURE", false, nil
	}

	return 42, "FAILURE", false, nil
}

func (f *fakeAPI) DescribeRun(_ context.Context, _ []string, build int, _ bool) (*client.PipelineRun, error) {
	f.calls++
	if r, ok := f.runs[build]; ok {
		return r, nil
	}

	return nil, &client.APIError{Status: 404, Reason: "no Pipeline stage data"}
}

func (f *fakeAPI) DescribeNode(_ context.Context, _ []string, _ int, id string) (*client.Stage, error) {
	f.calls++
	if n, ok := f.nodes[id]; ok {
		return n, nil
	}

	return nil, &client.APIError{Status: 404, Reason: "x"}
}

func (f *fakeAPI) NodeLogTail(_ context.Context, _ []string, _ int, id string) (*client.NodeLog, error) {
	f.calls++
	if l, ok := f.nodeLogs[id]; ok {
		return l, nil
	}

	return &client.NodeLog{NodeID: id}, nil
}

func (f *fakeAPI) TestReport(_ context.Context, _ []string, build int, tree string) (*client.TestReport, error) {
	f.calls++
	f.trees = append(f.trees, tree)
	if r, ok := f.reports[build]; ok {
		return r, nil
	}

	return nil, &client.APIError{Status: 404, Reason: "no test report for this build (JUnit results were not published)"}
}

func (f *fakeAPI) BuildHistory(_ context.Context, p []string, limit int) ([]client.BuildSummary, error) {
	f.calls++
	f.path, f.limit = p, limit

	return f.history, f.err
}

func (f *fakeAPI) ConsoleText(_ context.Context, p []string, ref string, start int64, maxBytes int) (*client.Console, error) {
	f.calls++
	f.path, f.ref, f.start, f.maxBytes = p, ref, start, maxBytes
	f.refs = append(f.refs, ref)

	return f.console, f.err
}

func (f *fakeAPI) Queue(context.Context) ([]client.QueueItem, error) {
	f.calls++

	return f.queue, f.err
}

func (f *fakeAPI) GetArtifact(_ context.Context, p []string, ref, rel string, _ int) ([]byte, bool, error) {
	f.calls++
	f.refs = append(f.refs, ref)
	c, ok := f.files[rel]
	if !ok {
		return nil, false, &client.APIError{Status: 404, Reason: "x"}
	}

	return []byte(c), false, nil
}

func session(t *testing.T, api API, opts ...Options) *mcp.ClientSession {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	New(api, opts...).Register(s)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil {
		if res.IsError {
			t.Fatalf("%s: unexpected error %v", name, res.Content[0])
		}
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}

	return res
}

func code(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatal("expected error")
	}
	var env struct {
		Error toolkit.ToolError `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &env); err != nil {
		t.Fatalf("not an error envelope: %v", res.Content[0])
	}

	return env.Error.Code
}

func TestToolsRegisteredReadOnly(t *testing.T) {
	res, err := session(t, &fakeAPI{}).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		if !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s not read-only", tl.Name)
		}
		if tl.Name == "jenkins_list_jobs" && !strings.Contains(tl.Description, "'TeamA' folder") {
			t.Errorf("description should name the base folder: %s", tl.Description)
		}
	}
	want := "jenkins_analyze_build_logs,jenkins_compare_builds,jenkins_get_build_changes,jenkins_get_build_history,jenkins_get_build_images,jenkins_get_build_info,jenkins_get_console_logs,jenkins_get_failed_stages,jenkins_get_failed_step_logs,jenkins_get_job_info,jenkins_get_queue_info,jenkins_get_stage_details,jenkins_get_test_failures,jenkins_get_test_results,jenkins_list_jobs,jenkins_whoami"
	if strings.Join(names, ",") != want {
		t.Fatalf("%v", names)
	}
}

func TestListJobs(t *testing.T) {
	api := &fakeAPI{jobs: []client.JobSummary{
		{Class: "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject", Name: "svc-b", FullName: "TeamA/team/svc-b"},
		{Class: "org.jenkinsci.plugins.workflow.job.WorkflowJob", Name: "svc-a", FullName: "TeamA/team/svc-a", Color: "red_anime", Buildable: true,
			LastBuild: &client.BuildRef{Number: 7, Result: "FAILURE", Timestamp: 1700000000000}},
	}}
	var out listJobsOutput
	call(t, session(t, api), "jenkins_list_jobs", map[string]any{"folder": "/team/"}, &out)
	if strings.Join(api.path, "/") != "team" || out.Count != 2 || out.BaseFolder != "TeamA" {
		t.Fatalf("%+v %v", out, api.path)
	}
	a, b := out.Jobs[0], out.Jobs[1]
	if a.Path != "team/svc-a" || a.Kind != "pipeline" || a.Status != "FAILURE" || !a.Building || a.LastBuildAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("%+v", a)
	}
	if !b.IsFolder || b.Kind != "multibranch-pipeline" {
		t.Fatalf("%+v", b)
	}
}

func TestValidationRejectsBeforeCall(t *testing.T) {
	api := &fakeAPI{}
	cs := session(t, api)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"jenkins_get_job_info", map[string]any{"job": ""}},
		{"jenkins_get_job_info", map[string]any{"job": "../other-team"}},
		{"jenkins_get_job_info", map[string]any{"job": "svc//main"}},
		{"jenkins_list_jobs", map[string]any{"folder": "a/../../b"}},
		{"jenkins_get_build_info", map[string]any{"job": "svc", "build": "-1"}},
		{"jenkins_get_build_info", map[string]any{"job": "svc", "build": "lastBuild/../x"}},
		{"jenkins_get_build_history", map[string]any{"job": "svc", "limit": 500}},
		{"jenkins_get_console_logs", map[string]any{"job": "svc", "maxBytes": 5 << 20}},
		{"jenkins_get_console_logs", map[string]any{"job": "svc", "start": -5}},
		{"jenkins_get_job_info", map[string]any{"job": "svc\nX"}},
	} {
		if c := code(t, call(t, cs, tc.tool, tc.args, nil)); c != toolkit.CodeInvalidInput {
			t.Errorf("%s %v: %s", tc.tool, tc.args, c)
		}
	}
	if api.calls != 0 {
		t.Fatalf("API called %d times", api.calls)
	}
}

func TestGetJobInfo(t *testing.T) {
	j := &client.Job{Class: "x.WorkflowJob", Name: "svc", FullName: "TeamA/svc", Color: "blue", Buildable: true,
		LastFailedBuild: &client.BuildRef{Number: 3, Result: "FAILURE"}}
	j.Property = append(j.Property, struct {
		ParameterDefinitions []client.ParamDef `json:"parameterDefinitions,omitempty"`
	}{ParameterDefinitions: []client.ParamDef{{Name: "ENV", Type: "ChoiceParameterDefinition", Choices: []string{"dev", "qa"}}}})
	api := &fakeAPI{job: j}
	var out jobOutput
	call(t, session(t, api), "jenkins_get_job_info", map[string]any{"job": "job/svc"}, &out)
	if strings.Join(api.path, "/") != "svc" || out.Status != "SUCCESS" || out.Parameters[0].Type != "Choice" || out.LastFailedBuild.Number != 3 {
		t.Fatalf("%+v", out)
	}
}

func TestGetBuildInfo(t *testing.T) {
	var b client.Build
	_ = json.Unmarshal([]byte(`{"number":42,"result":"FAILURE","duration":90500,"timestamp":1700000000000,"url":"u",
		"actions":[{"parameters":[{"name":"ENV","value":"qa"},{"name":"DRY","value":true}]},
		{"causes":[{"shortDescription":"Started by user Jane","userId":"jane"}]},
		{"lastBuiltRevision":{"SHA1":"abc","branch":[{"name":"origin/main"}]},"remoteUrls":["https://git.example.com/r.git"]}],
		"changeSets":[{"items":[{"commitId":"abc","msg":"fix\n","author":{"fullName":"Jane"}}]}]}`), &b)
	api := &fakeAPI{build: &b}
	var out buildOutput
	call(t, session(t, api), "jenkins_get_build_info", map[string]any{"job": "svc/main"}, &out)
	if api.ref != "lastBuild" || out.Number != 42 || out.DurationSeconds != 90.5 || out.Parameters["ENV"] != "qa" ||
		out.StartedBy != "jane" || out.GitCommit != "abc" || out.Changes[0].Message != "fix" {
		t.Fatalf("%+v", out)
	}
	call(t, session(t, api), "jenkins_get_build_info", map[string]any{"job": "svc/main", "build": "lastFailedBuild"}, &out)
	if api.ref != "lastFailedBuild" {
		t.Fatal(api.ref)
	}
}

func TestHistoryAndQueue(t *testing.T) {
	api := &fakeAPI{
		history: []client.BuildSummary{{Number: 1, Result: "SUCCESS"}, {Number: 2, Building: true}},
		queue:   []client.QueueItem{{ID: 9, Why: "Waiting for next available executor", Stuck: true}},
	}
	cs := session(t, api)
	var h historyOutput
	call(t, cs, "jenkins_get_build_history", map[string]any{"job": "svc", "limit": 5}, &h)
	if api.limit != 5 || h.Builds[0].Number != 2 || h.Builds[0].Result != "BUILDING" {
		t.Fatalf("%+v", h)
	}
	var q queueOutput
	call(t, cs, "jenkins_get_queue_info", map[string]any{}, &q)
	if q.Count != 1 || !q.Items[0].Stuck || q.BaseFolder != "TeamA" {
		t.Fatalf("%+v", q)
	}
}

func TestConsoleDefaultsToTail(t *testing.T) {
	api := &fakeAPI{console: &client.Console{Text: "BUILD FAILED", Start: 100, NextStart: 112, Size: 112}}
	cs := session(t, api)
	var out consoleOutput
	call(t, cs, "jenkins_get_console_logs", map[string]any{"job": "svc", "build": "42"}, &out)
	if api.start != -1 || api.maxBytes != defaultConsole || out.Text != "BUILD FAILED" || out.NextStart != 112 {
		t.Fatalf("%+v start=%d", out, api.start)
	}
	call(t, cs, "jenkins_get_console_logs", map[string]any{"job": "svc", "start": 0, "maxBytes": 1000}, &out)
	if api.start != 0 || api.maxBytes != 1000 || api.ref != "lastBuild" {
		t.Fatalf("start=%d max=%d ref=%s", api.start, api.maxBytes, api.ref)
	}
}

func TestErrorCodesAndWhoAmI(t *testing.T) {
	for status, want := range map[int]string{401: toolkit.CodeUnauthorized, 302: toolkit.CodeUnauthorized, 404: toolkit.CodeNotFound} {
		cs := session(t, &fakeAPI{err: &client.APIError{Status: status, Reason: "x"}})
		if c := code(t, call(t, cs, "jenkins_get_job_info", map[string]any{"job": "svc"}, nil)); c != want {
			t.Errorf("%d: %s", status, c)
		}
	}
	cs := session(t, &fakeAPI{err: &client.APIError{Status: 403, Reason: "user jane lacks hudson.model.Item.Read"}})
	if c := code(t, call(t, cs, "jenkins_get_job_info", map[string]any{"job": "svc"}, nil)); c != toolkit.CodeForbidden {
		t.Errorf("403: %s", c)
	}

	var who whoamiOutput
	call(t, session(t, &fakeAPI{who: &client.WhoAmI{Name: "jane", Authenticated: true}}), "jenkins_whoami", map[string]any{}, &who)
	if who.Name != "jane" || who.BaseFolder != "TeamA" {
		t.Fatalf("%+v", who)
	}
	// Jenkins answering as anonymous means the session cookie was not accepted.
	cs = session(t, &fakeAPI{who: &client.WhoAmI{Name: "anonymous", Anonymous: true}})
	if c := code(t, call(t, cs, "jenkins_whoami", map[string]any{}, nil)); c != toolkit.CodeUnauthorized {
		t.Errorf("anonymous whoami: %s", c)
	}
}
