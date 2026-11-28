// Package jenkins defines the read-only Jenkins MCP tools.
package jenkins

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	authjenkins "github.com/vinaycharlie01/jenkins-mcp-go/internal/auth/jenkins"
	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

// API is the subset of the Jenkins client the tools use.
type API interface {
	BaseFolder() string
	RelPath(fullName string) string
	WhoAmI(ctx context.Context) (*client.WhoAmI, error)
	ListJobs(ctx context.Context, path []string) ([]client.JobSummary, error)
	GetJob(ctx context.Context, path []string) (*client.Job, error)
	GetBuild(ctx context.Context, path []string, ref string) (*client.Build, error)
	BuildHistory(ctx context.Context, path []string, limit int) ([]client.BuildSummary, error)
	ConsoleText(ctx context.Context, path []string, ref string, start int64, maxBytes int) (*client.Console, error)
	Queue(ctx context.Context) ([]client.QueueItem, error)
}

// Tools holds the dependencies of the Jenkins tools.
type Tools struct{ api API }

// New returns the Jenkins tool set.
func New(api API) *Tools { return &Tools{api: api} }

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(true)}

func ptr[T any](v T) *T { return &v }

const pathHelp = "Job path relative to the configured base folder, slash-separated, e.g. my-service/main. " +
	"Multibranch branch names containing '/' are written with %2F as Jenkins shows them (feature%2Fx). " +
	"Use the 'path' values returned by jenkins_list_jobs."

// Register adds every Jenkins tool to s.
func (t *Tools) Register(s *mcp.Server) {
	scope := "the Jenkins root"
	if f := t.api.BaseFolder(); f != "" {
		scope = "the '" + f + "' folder"
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_whoami", Title: "Jenkins: who am I",
		Description: "Show which Jenkins user the configured credential authenticates as (use to verify the session).",
		Annotations: readOnly,
	}, t.whoami)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_list_jobs", Title: "Jenkins: list jobs",
		Description: "List jobs and folders inside " + scope + " (or a sub-folder) with last build status. Folders can be listed by passing their path.",
		Annotations: readOnly,
	}, t.listJobs)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_job_info", Title: "Jenkins: job details",
		Description: "Get a job's details: type, status, health, last/last successful/last failed builds, parameter definitions, and child jobs for folders/multibranch.",
		Annotations: readOnly,
	}, t.getJob)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_build_info", Title: "Jenkins: build details",
		Description: "Get a build's details: result, duration, parameters, causes (who started it), Git revision and changes. Build is a number or lastBuild, lastFailedBuild, lastSuccessfulBuild, ...",
		Annotations: readOnly,
	}, t.getBuild)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_build_history", Title: "Jenkins: build history",
		Description: "Get a job's recent builds, newest first, with result, start time and duration.",
		Annotations: readOnly,
	}, t.getHistory)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_console_logs", Title: "Jenkins: console output",
		Description: "Get a build's console output. By default returns the last maxBytes (where failures usually are). " +
			"Pass start to read from an offset and page forward with the returned nextStart.",
		Annotations: readOnly,
	}, t.getConsole)
	mcp.AddTool(s, &mcp.Tool{
		Name: "jenkins_get_queue_info", Title: "Jenkins: build queue",
		Description: "List builds waiting in the Jenkins queue for jobs in " + scope + ", with the reason they are waiting.",
		Annotations: readOnly,
	}, t.getQueue)
}

func classify(err error) string {
	switch {
	case errors.Is(err, client.ErrUnauthorized):
		return toolkit.CodeUnauthorized
	case errors.Is(err, client.ErrForbidden):
		return toolkit.CodeForbidden
	case errors.Is(err, client.ErrNotFound):
		return toolkit.CodeNotFound
	case errors.Is(err, authjenkins.ErrNoCredential):
		return toolkit.CodeNotConfigured
	}

	return ""
}

func wrap(err error) error { return toolkit.Wrap(err, classify) }
