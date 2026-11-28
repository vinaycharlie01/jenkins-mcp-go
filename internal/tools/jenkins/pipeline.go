package jenkins

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

const (
	maxParamDesc   = 500
	defaultLogStep = 3
	maxLogSteps    = 10
)

func (t *Tools) registerDebugTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_failed_stages", Title: "Jenkins: failed Pipeline stages",
		Description: "List the stages of a Pipeline build with status and duration, and highlight the failed/unstable/aborted ones with their " +
			"error message. Start here when a Pipeline build fails. Build defaults to lastBuild (use lastFailedBuild for the latest failure).",
		Annotations: readOnly,
	}, t.failedStages)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_stage_details", Title: "Jenkins: Pipeline stage steps",
		Description: "Show the steps of one Pipeline stage (by name or id): step name, command/argument, status, duration and error.",
		Annotations: readOnly,
	}, t.stageDetails)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_failed_step_logs", Title: "Jenkins: logs of failed steps",
		Description: "Return the END of the log of each failed step (Jenkins returns about the last 10 KB per step) together with the step's " +
			"command and error, for failed stages or one given stage. The fastest way to the actual error message.",
		Annotations: readOnly,
	}, t.failedStepLogs)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_test_results", Title: "Jenkins: test results summary",
		Description: "Summarize the build's JUnit test results: total, passed, failed, skipped, new failures (regressions) and fixed tests, " +
			"and the suites with the most failures.",
		Annotations: readOnly,
	}, t.testResults)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_test_failures", Title: "Jenkins: failed tests",
		Description: "List failed tests with error message, trimmed stack trace, and how long each has been failing (age; 1 = new in this build). " +
			"New failures (regressions) come first.",
		Annotations: readOnly,
	}, t.testFailures)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_build_changes", Title: "Jenkins: commits in a build",
		Description: "List the SCM changes a build picked up: commit, author, time, message and changed files, plus culprits and the Git revision built.",
		Annotations: readOnly,
	}, t.buildChanges)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_compare_builds", Title: "Jenkins: compare two builds",
		Description: "Compare two builds of a job (default: lastSuccessfulBuild vs lastBuild): result, duration, parameters, Git revision, " +
			"agent, per-stage status and duration, test counts, and the commits that came in between. Use it to find what changed between a good and a bad build.",
		Annotations: readOnly,
	}, t.compareBuilds)
}

// resolve validates job/build and pins the build number.
func (t *Tools) resolve(ctx context.Context, job, build string) ([]string, int, string, error) {
	path, err := jobPath("job", job, true)
	if err != nil {
		return nil, 0, "", err
	}
	ref, err := buildRef(build)
	if err != nil {
		return nil, 0, "", err
	}
	n, result, building, err := t.api.ResolveBuild(ctx, path, ref)
	if err != nil {
		return nil, 0, "", wrap(err)
	}
	if building {
		result = "BUILDING"
	}

	return path, n, result, nil
}

func secs(ms int64) float64 { return float64(ms/100) / 10 }

func failedStatus(s string) bool { return s == "FAILED" || s == "UNSTABLE" || s == "ABORTED" }

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}

	return s
}

func errMsg(e *client.ErrorInfo) string {
	if e == nil {
		return ""
	}

	return trim(e.Message, 2000)
}

// ---- jenkins_get_failed_stages ----

type buildRefInput struct {
	Job   string `json:"job" jsonschema:"Job path relative to the base folder, e.g. my-service/main."`
	Build string `json:"build,omitempty" jsonschema:"Build number, or lastBuild (default), lastFailedBuild, lastSuccessfulBuild, ..."`
}

// StageSummary is one stage of a run.
type StageSummary struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	DurationSeconds float64 `json:"durationSeconds"`
	Error           string  `json:"error,omitempty"`
	ErrorType       string  `json:"errorType,omitempty"`
}

type failedStagesOutput struct {
	Job             string         `json:"job"`
	Build           int            `json:"build"`
	Result          string         `json:"result,omitempty"`
	PipelineStatus  string         `json:"pipelineStatus"`
	DurationSeconds float64        `json:"durationSeconds"`
	FirstFailed     string         `json:"firstFailedStage,omitempty"`
	FailedStages    []StageSummary `json:"failedStages"`
	Stages          []StageSummary `json:"stages"`
	Notes           []string       `json:"notes,omitempty"`
}

func stageSummary(s client.Stage) StageSummary {
	out := StageSummary{ID: s.ID, Name: s.Name, Status: s.Status, DurationSeconds: secs(s.DurationMillis), Error: errMsg(s.Error)}
	if s.Error != nil {
		out.ErrorType = s.Error.Type
	}

	return out
}

func (t *Tools) failedStages(ctx context.Context, _ *mcp.CallToolRequest, in buildRefInput) (*mcp.CallToolResult, failedStagesOutput, error) {
	path, n, result, err := t.resolve(ctx, in.Job, in.Build)
	if err != nil {
		return nil, failedStagesOutput{}, err
	}
	run, err := t.api.DescribeRun(ctx, path, n, false)
	if err != nil {
		return nil, failedStagesOutput{}, wrap(err)
	}
	out := failedStagesOutput{Job: strings.Join(path, "/"), Build: n, Result: result, PipelineStatus: run.Status,
		DurationSeconds: secs(run.DurationMillis), FailedStages: []StageSummary{}, Stages: []StageSummary{}}
	for _, s := range run.Stages {
		sum := stageSummary(s)
		out.Stages = append(out.Stages, sum)
		if failedStatus(s.Status) {
			out.FailedStages = append(out.FailedStages, sum)
			if out.FirstFailed == "" {
				out.FirstFailed = s.Name
			}
		}
	}
	if len(out.FailedStages) == 0 && (result == "FAILURE" || result == "UNSTABLE" || result == "ABORTED") {
		out.Notes = append(out.Notes, "the build failed outside any stage (e.g. in a post section or before the first stage); "+
			"use jenkins_get_console_logs for the end of the full log")
	}
	if len(out.FailedStages) > 0 {
		out.Notes = append(out.Notes, "next: jenkins_get_failed_step_logs for the error output of the failed steps")
	}

	return nil, out, nil
}

// ---- jenkins_get_stage_details ----

type stageInput struct {
	buildRefInput
	Stage string `json:"stage" jsonschema:"Stage name (case-insensitive) or stage id from jenkins_get_failed_stages."`
}

// Step is one step of a stage.
type Step struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Argument        string  `json:"argument,omitempty"`
	Status          string  `json:"status"`
	DurationSeconds float64 `json:"durationSeconds"`
	Error           string  `json:"error,omitempty"`
}

type stageOutput struct {
	Job             string   `json:"job"`
	Build           int      `json:"build"`
	Stage           string   `json:"stage"`
	StageID         string   `json:"stageId"`
	Status          string   `json:"status"`
	Agent           string   `json:"agent,omitempty"`
	DurationSeconds float64  `json:"durationSeconds"`
	PausedSeconds   float64  `json:"pausedSeconds,omitempty"`
	Error           string   `json:"error,omitempty"`
	Steps           []Step   `json:"steps"`
	FailedSteps     int      `json:"failedSteps"`
	Notes           []string `json:"notes,omitempty"`
}

// findStage matches a stage by id or case-insensitive name.
func findStage(run *client.PipelineRun, want string) (client.Stage, []string, error) {
	var notes []string
	var match []client.Stage
	for _, s := range run.Stages {
		if s.ID == want || strings.EqualFold(s.Name, want) {
			match = append(match, s)
		}
	}
	if len(match) == 0 {
		names := make([]string, 0, len(run.Stages))
		for _, s := range run.Stages {
			names = append(names, s.Name)
		}

		return client.Stage{}, nil, &toolkit.ToolError{Code: toolkit.CodeNotFound,
			Message: fmt.Sprintf("stage %q not found; stages: %s", want, strings.Join(names, ", "))}
	}
	if len(match) > 1 {
		notes = append(notes, fmt.Sprintf("%d stages are named %q; showing id %s (pass the id to choose)", len(match), want, match[0].ID))
	}

	return match[0], notes, nil
}

func stepOf(n client.FlowNode) Step {
	return Step{ID: n.ID, Name: n.Name, Argument: trim(n.ParameterDescription, maxParamDesc), Status: n.Status,
		DurationSeconds: secs(n.DurationMillis), Error: errMsg(n.Error)}
}

func (t *Tools) stageDetails(ctx context.Context, _ *mcp.CallToolRequest, in stageInput) (*mcp.CallToolResult, stageOutput, error) {
	if err := toolkit.Text("stage", in.Stage, 255); err != nil {
		return nil, stageOutput{}, err
	}
	if strings.TrimSpace(in.Stage) == "" {
		return nil, stageOutput{}, toolkit.Invalid("stage is required")
	}
	path, n, _, err := t.resolve(ctx, in.Job, in.Build)
	if err != nil {
		return nil, stageOutput{}, err
	}
	run, err := t.api.DescribeRun(ctx, path, n, false)
	if err != nil {
		return nil, stageOutput{}, wrap(err)
	}
	st, notes, err := findStage(run, strings.TrimSpace(in.Stage))
	if err != nil {
		return nil, stageOutput{}, err
	}
	node, err := t.api.DescribeNode(ctx, path, n, st.ID)
	if err != nil {
		return nil, stageOutput{}, wrap(err)
	}
	out := stageOutput{Job: strings.Join(path, "/"), Build: n, Stage: node.Name, StageID: node.ID, Status: node.Status,
		Agent: node.ExecNode, DurationSeconds: secs(node.DurationMillis), PausedSeconds: secs(node.PauseDurationMillis),
		Error: errMsg(node.Error), Steps: []Step{}, Notes: notes}
	for _, s := range node.StageFlowNodes {
		out.Steps = append(out.Steps, stepOf(s))
		if failedStatus(s.Status) || s.Error != nil {
			out.FailedSteps++
		}
	}

	return nil, out, nil
}

// ---- jenkins_get_failed_step_logs ----

type stepLogsInput struct {
	buildRefInput
	Stage    string `json:"stage,omitempty" jsonschema:"Only this stage (name or id). Default: all failed stages."`
	MaxSteps int    `json:"maxSteps,omitempty" jsonschema:"Maximum steps to return logs for (default 3, max 10)."`
}

// StepLog is the log tail of one failed step.
type StepLog struct {
	Stage     string `json:"stage"`
	Step      string `json:"step"`
	StepID    string `json:"stepId"`
	Argument  string `json:"argument,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Log       string `json:"log"`
	Truncated bool   `json:"logTruncated"`
}

type stepLogsOutput struct {
	Job   string    `json:"job"`
	Build int       `json:"build"`
	Steps []StepLog `json:"steps"`
	Notes []string  `json:"notes,omitempty"`
}

func (t *Tools) failedStepLogs(ctx context.Context, _ *mcp.CallToolRequest, in stepLogsInput) (*mcp.CallToolResult, stepLogsOutput, error) {
	if err := toolkit.Text("stage", in.Stage, 255); err != nil {
		return nil, stepLogsOutput{}, err
	}
	maxSteps, err := toolkit.Limit("maxSteps", in.MaxSteps, defaultLogStep, maxLogSteps)
	if err != nil {
		return nil, stepLogsOutput{}, err
	}
	path, n, _, err := t.resolve(ctx, in.Job, in.Build)
	if err != nil {
		return nil, stepLogsOutput{}, err
	}
	run, err := t.api.DescribeRun(ctx, path, n, true)
	if err != nil {
		return nil, stepLogsOutput{}, wrap(err)
	}
	out := stepLogsOutput{Job: strings.Join(path, "/"), Build: n, Steps: []StepLog{}}

	var stages []client.Stage
	if s := strings.TrimSpace(in.Stage); s != "" {
		st, notes, err := findStage(run, s)
		if err != nil {
			return nil, stepLogsOutput{}, err
		}
		out.Notes = append(out.Notes, notes...)
		stages = []client.Stage{st}
	} else {
		for _, st := range run.Stages {
			if failedStatus(st.Status) {
				stages = append(stages, st)
			}
		}
	}
	if len(stages) == 0 {
		out.Notes = append(out.Notes, "no failed stage; if the build still failed, use jenkins_get_console_logs for the end of the full log")

		return nil, out, nil
	}

	for _, st := range stages {
		steps := st.StageFlowNodes
		if steps == nil { // older Stage View: fetch the stage's steps
			node, err := t.api.DescribeNode(ctx, path, n, st.ID)
			if err != nil {
				return nil, stepLogsOutput{}, wrap(err)
			}
			steps = node.StageFlowNodes
		}
		var picked []client.FlowNode
		for _, s := range steps {
			if failedStatus(s.Status) || s.Error != nil {
				picked = append(picked, s)
			}
		}
		if len(picked) == 0 && len(steps) > 0 && in.Stage != "" {
			picked = steps[len(steps)-1:] // a chosen stage without failures: show its last step
		}
		for _, s := range picked {
			if len(out.Steps) >= maxSteps {
				out.Notes = append(out.Notes, fmt.Sprintf("more failed steps exist; raise maxSteps (max %d)", maxLogSteps))

				return nil, out, nil
			}
			l, err := t.api.NodeLogTail(ctx, path, n, s.ID)
			if err != nil {
				return nil, stepLogsOutput{}, wrap(err)
			}
			out.Steps = append(out.Steps, StepLog{Stage: st.Name, Step: s.Name, StepID: s.ID, Argument: trim(s.ParameterDescription, maxParamDesc),
				Status: s.Status, Error: errMsg(s.Error), Log: l.Text, Truncated: l.HasMore})
		}
	}
	if len(out.Steps) == 0 {
		out.Notes = append(out.Notes, "the failed stages have no failed step of their own (e.g. a failed parallel branch or post step); use jenkins_get_console_logs")
	}
	for _, s := range out.Steps {
		if s.Truncated {
			out.Notes = append(out.Notes, "logs are the end of each step's output (Jenkins limits this API to ~10 KB per step); use jenkins_get_console_logs for more")

			break
		}
	}

	return nil, out, nil
}
