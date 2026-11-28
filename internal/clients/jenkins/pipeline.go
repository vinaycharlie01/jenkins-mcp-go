package jenkins

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
)

// Pipeline data comes from the Pipeline Stage View plugin REST API (wfapi),
// test data from the JUnit plugin (testReport).

// ErrorInfo is a flow node error.
type ErrorInfo struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

// FlowNode is a step (or stage) in a Pipeline run.
type FlowNode struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	ExecNode             string     `json:"execNode,omitempty"`
	Status               string     `json:"status"` // SUCCESS, FAILED, UNSTABLE, ABORTED, IN_PROGRESS, NOT_EXECUTED, PAUSED_PENDING_INPUT
	ParameterDescription string     `json:"parameterDescription,omitempty"`
	Error                *ErrorInfo `json:"error,omitempty"`
	StartTimeMillis      int64      `json:"startTimeMillis"`
	DurationMillis       int64      `json:"durationMillis"`
	PauseDurationMillis  int64      `json:"pauseDurationMillis"`
	ParentNodes          []string   `json:"parentNodes,omitempty"`
}

// Stage is a stage with (when requested) its steps.
type Stage struct {
	FlowNode
	StageFlowNodes []FlowNode `json:"stageFlowNodes,omitempty"`
}

// PipelineRun is wfapi/describe of a run.
type PipelineRun struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Status              string  `json:"status"`
	StartTimeMillis     int64   `json:"startTimeMillis"`
	DurationMillis      int64   `json:"durationMillis"`
	QueueDurationMillis int64   `json:"queueDurationMillis"`
	Stages              []Stage `json:"stages"`
}

// NodeLog is wfapi/log of a flow node: the END of its log (Jenkins returns
// at most ~10 KiB by default), converted to plain text.
type NodeLog struct {
	NodeID     string `json:"nodeId"`
	NodeStatus string `json:"nodeStatus"`
	Length     int64  `json:"length"`
	HasMore    bool   `json:"hasMore"`
	Text       string `json:"text"`
}

var nodeIDRe = regexp.MustCompile(`^[0-9]{1,9}$`)

// ResolveBuild turns a build reference (number or permalink) into its number,
// so several calls read the same build.
func (c *Client) ResolveBuild(ctx context.Context, path []string, ref string) (int, string, bool, error) {
	if len(path) == 0 || !ValidBuildRef(ref) {
		return 0, "", false, fmt.Errorf("invalid job path or build %q", ref)
	}
	u, err := c.jobURL(path, ref, "api", "json")
	if err != nil {
		return 0, "", false, err
	}
	var b struct {
		Number   int    `json:"number"`
		Result   string `json:"result"`
		Building bool   `json:"building"`
	}
	if err := c.getJSON(ctx, u, "number,result,building", &b); err != nil {
		return 0, "", false, err
	}

	return b.Number, b.Result, b.Building, nil
}

// DescribeRun returns the Pipeline stages of a run (fullStages adds steps).
func (c *Client) DescribeRun(ctx context.Context, path []string, build int, fullStages bool) (*PipelineRun, error) {
	u, err := c.jobURL(path, strconv.Itoa(build), "wfapi", "describe")
	if err != nil {
		return nil, err
	}
	if fullStages {
		u.RawQuery = url.Values{"fullStages": {"true"}}.Encode()
	}
	var r PipelineRun
	if err := c.getJSON(ctx, u, "", &r); err != nil {
		return nil, pipelineErr(err)
	}

	return &r, nil
}

// DescribeNode returns one stage node with its steps.
func (c *Client) DescribeNode(ctx context.Context, path []string, build int, nodeID string) (*Stage, error) {
	if !nodeIDRe.MatchString(nodeID) {
		return nil, fmt.Errorf("invalid node id %q", nodeID)
	}
	u, err := c.jobURL(path, strconv.Itoa(build), "execution", "node", nodeID, "wfapi", "describe")
	if err != nil {
		return nil, err
	}
	var s Stage
	if err := c.getJSON(ctx, u, "", &s); err != nil {
		return nil, pipelineErr(err)
	}

	return &s, nil
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// NodeLogTail returns the end of a flow node's log as plain text.
func (c *Client) NodeLogTail(ctx context.Context, path []string, build int, nodeID string) (*NodeLog, error) {
	if !nodeIDRe.MatchString(nodeID) {
		return nil, fmt.Errorf("invalid node id %q", nodeID)
	}
	u, err := c.jobURL(path, strconv.Itoa(build), "execution", "node", nodeID, "wfapi", "log")
	if err != nil {
		return nil, err
	}
	var l NodeLog
	if err := c.getJSON(ctx, u, "", &l); err != nil {
		return nil, pipelineErr(err)
	}
	// wfapi returns the log as HTML (console annotations); make it plain text.
	l.Text = html.UnescapeString(tagRe.ReplaceAllString(l.Text, ""))

	return &l, nil
}

func pipelineErr(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return &APIError{Status: 404, Reason: "no Pipeline stage data: the job is not a Pipeline, the build does not exist, or the Pipeline Stage View plugin (wfapi) is not installed"}
	}

	return err
}

// TestCase is one JUnit test case.
type TestCase struct {
	ClassName       string  `json:"className"`
	Name            string  `json:"name"`
	Status          string  `json:"status"` // PASSED, SKIPPED, FAILED, FIXED, REGRESSION
	Duration        float64 `json:"duration"`
	ErrorDetails    string  `json:"errorDetails,omitempty"`
	ErrorStackTrace string  `json:"errorStackTrace,omitempty"`
	Age             int     `json:"age"`
	FailedSince     int     `json:"failedSince"`
}

// TestSuite is one JUnit suite.
type TestSuite struct {
	Name     string     `json:"name"`
	Duration float64    `json:"duration"`
	Cases    []TestCase `json:"cases"`
}

// TestReport is the build's JUnit report.
type TestReport struct {
	Duration  float64     `json:"duration"`
	FailCount int         `json:"failCount"`
	PassCount int         `json:"passCount"`
	SkipCount int         `json:"skipCount"`
	Empty     bool        `json:"empty"`
	Suites    []TestSuite `json:"suites"`
}

// Test report projections.
const (
	TestTreeSummary = "duration,failCount,passCount,skipCount,empty,suites[name,duration,cases[status]]"
	TestTreeFailed  = "failCount,passCount,skipCount,suites[name,cases[className,name,status,duration,errorDetails,errorStackTrace,age,failedSince]]"
	TestTreeCounts  = "duration,failCount,passCount,skipCount"
)

// TestReport returns the JUnit report of a build with the given projection.
func (c *Client) TestReport(ctx context.Context, path []string, build int, tree string) (*TestReport, error) {
	u, err := c.jobURL(path, strconv.Itoa(build), "testReport", "api", "json")
	if err != nil {
		return nil, err
	}
	var r TestReport
	if err := c.getJSON(ctx, u, tree, &r); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return nil, &APIError{Status: 404, Reason: "no test report for this build (JUnit results were not published)"}
		}

		return nil, err
	}

	return &r, nil
}
