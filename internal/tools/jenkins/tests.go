package jenkins

import (
	"context"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

const (
	maxSuitesShown   = 20
	defaultFailures  = 25
	maxFailures      = 200
	defaultStackLine = 15
	maxStackLines    = 100
	maxErrorDetails  = 2000
)

func isFailed(status string) bool { return status == "FAILED" || status == "REGRESSION" }

// SuiteSummary counts one suite.
type SuiteSummary struct {
	Name            string  `json:"name"`
	Tests           int     `json:"tests"`
	Failed          int     `json:"failed"`
	Skipped         int     `json:"skipped"`
	DurationSeconds float64 `json:"durationSeconds"`
}

type testResultsOutput struct {
	Job             string         `json:"job"`
	Build           int            `json:"build"`
	Total           int            `json:"total"`
	Passed          int            `json:"passed"`
	Failed          int            `json:"failed"`
	Skipped         int            `json:"skipped"`
	NewFailures     int            `json:"newFailures"`
	Fixed           int            `json:"fixed"`
	DurationSeconds float64        `json:"durationSeconds"`
	FailingSuites   []SuiteSummary `json:"failingSuites"`
	Suites          int            `json:"suites"`
}

func (t *Tools) testResults(ctx context.Context, _ *mcp.CallToolRequest, in buildRefInput) (*mcp.CallToolResult, testResultsOutput, error) {
	path, n, _, err := t.resolve(ctx, in.Job, in.Build)
	if err != nil {
		return nil, testResultsOutput{}, err
	}
	r, err := t.api.TestReport(ctx, path, n, client.TestTreeSummary)
	if err != nil {
		return nil, testResultsOutput{}, wrap(err)
	}
	out := testResultsOutput{Job: strings.Join(path, "/"), Build: n, Passed: r.PassCount, Failed: r.FailCount, Skipped: r.SkipCount,
		DurationSeconds: r.Duration, Suites: len(r.Suites), FailingSuites: []SuiteSummary{}}
	out.Total = out.Passed + out.Failed + out.Skipped
	for _, s := range r.Suites {
		sum := SuiteSummary{Name: s.Name, Tests: len(s.Cases), DurationSeconds: s.Duration}
		for _, c := range s.Cases {
			switch c.Status {
			case "REGRESSION":
				out.NewFailures++
				sum.Failed++
			case "FAILED":
				sum.Failed++
			case "FIXED":
				out.Fixed++
			case "SKIPPED":
				sum.Skipped++
			}
		}
		if sum.Failed > 0 {
			out.FailingSuites = append(out.FailingSuites, sum)
		}
	}
	sort.Slice(out.FailingSuites, func(i, j int) bool { return out.FailingSuites[i].Failed > out.FailingSuites[j].Failed })
	if len(out.FailingSuites) > maxSuitesShown {
		out.FailingSuites = out.FailingSuites[:maxSuitesShown]
	}

	return nil, out, nil
}

type testFailuresInput struct {
	buildRefInput
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum failed tests to return (default 25, max 200)."`
	StackLines int    `json:"stackLines,omitempty" jsonschema:"Stack trace lines per test (default 15, max 100)."`
	Search     string `json:"search,omitempty" jsonschema:"Only tests whose class or name contains this text (case-insensitive)."`
}

// TestFailure is one failed test.
type TestFailure struct {
	Suite            string  `json:"suite"`
	ClassName        string  `json:"className"`
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	New              bool    `json:"new"`
	FailingForBuilds int     `json:"failingForBuilds"`
	FailedSince      int     `json:"failedSince,omitempty"`
	DurationSeconds  float64 `json:"durationSeconds"`
	Error            string  `json:"error,omitempty"`
	StackTrace       string  `json:"stackTrace,omitempty"`
}

type testFailuresOutput struct {
	Job         string        `json:"job"`
	Build       int           `json:"build"`
	TotalFailed int           `json:"totalFailed"`
	NewFailures int           `json:"newFailures"`
	Returned    int           `json:"returned"`
	Failures    []TestFailure `json:"failures"`
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}

	return strings.Join(lines, "\n")
}

func (t *Tools) testFailures(ctx context.Context, _ *mcp.CallToolRequest, in testFailuresInput) (*mcp.CallToolResult, testFailuresOutput, error) {
	limit, err := toolkit.Limit("limit", in.Limit, defaultFailures, maxFailures)
	if err != nil {
		return nil, testFailuresOutput{}, err
	}
	stack, err := toolkit.Limit("stackLines", in.StackLines, defaultStackLine, maxStackLines)
	if err != nil {
		return nil, testFailuresOutput{}, err
	}
	if err := toolkit.Text("search", in.Search, 255); err != nil {
		return nil, testFailuresOutput{}, err
	}
	path, n, _, err := t.resolve(ctx, in.Job, in.Build)
	if err != nil {
		return nil, testFailuresOutput{}, err
	}
	r, err := t.api.TestReport(ctx, path, n, client.TestTreeFailed)
	if err != nil {
		return nil, testFailuresOutput{}, wrap(err)
	}
	q := strings.ToLower(in.Search)
	var all []TestFailure
	for _, s := range r.Suites {
		for _, c := range s.Cases {
			if !isFailed(c.Status) {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(c.ClassName+"."+c.Name), q) {
				continue
			}
			all = append(all, TestFailure{
				Suite: s.Name, ClassName: c.ClassName, Name: c.Name, Status: c.Status,
				New: c.Status == "REGRESSION" || c.Age == 1, FailingForBuilds: c.Age, FailedSince: c.FailedSince,
				DurationSeconds: c.Duration, Error: trim(c.ErrorDetails, maxErrorDetails), StackTrace: firstLines(c.ErrorStackTrace, stack),
			})
		}
	}
	// New failures first, then the most recently started failures.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].New != all[j].New {
			return all[i].New
		}

		return all[i].FailingForBuilds < all[j].FailingForBuilds
	})
	out := testFailuresOutput{Job: strings.Join(path, "/"), Build: n, TotalFailed: len(all), Failures: []TestFailure{}}
	for _, f := range all {
		if f.New {
			out.NewFailures++
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	out.Failures = append(out.Failures, all...)
	out.Returned = len(out.Failures)

	return nil, out, nil
}
