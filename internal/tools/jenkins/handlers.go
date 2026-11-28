package jenkins

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/config"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

const (
	maxPathLen     = 1024
	defaultConsole = 64 << 10
	maxConsole     = 1 << 20
)

// jobPath validates and splits a tool's job path.
func jobPath(field, p string, required bool) ([]string, error) {
	if err := toolkit.Text(field, p, maxPathLen); err != nil {
		return nil, err
	}
	parts, err := config.SplitJobPath(p)
	if err != nil {
		return nil, toolkit.Invalid("%s: %v", field, err)
	}
	if required && len(parts) == 0 {
		return nil, toolkit.Invalid("%s is required", field)
	}

	return parts, nil
}

func buildRef(b string) (string, error) {
	if b == "" {
		return "lastBuild", nil
	}
	if !client.ValidBuildRef(b) {
		return "", toolkit.Invalid("build must be a positive build number or one of lastBuild, lastSuccessfulBuild, lastFailedBuild, lastCompletedBuild, lastStableBuild, lastUnstableBuild, lastUnsuccessfulBuild")
	}

	return b, nil
}

// millisToRFC3339 formats a Jenkins epoch-millis timestamp.
func millisToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}

	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// statusFromColor maps Jenkins' ball color to a readable status.
func statusFromColor(color string) (status string, building bool) {
	building = strings.HasSuffix(color, "_anime")
	switch strings.TrimSuffix(color, "_anime") {
	case "blue", "green":
		return "SUCCESS", building
	case "red":
		return "FAILURE", building
	case "yellow":
		return "UNSTABLE", building
	case "aborted":
		return "ABORTED", building
	case "disabled":
		return "DISABLED", building
	case "notbuilt", "nobuilt":
		return "NOT_BUILT", building
	case "", "grey":
		return "", building
	}

	return strings.ToUpper(color), building
}

// kindFromClass shortens a Jenkins _class to a readable kind.
func kindFromClass(class string) (kind string, folder bool) {
	short := class[strings.LastIndex(class, ".")+1:]
	switch short {
	case "Folder":
		return "folder", true
	case "OrganizationFolder":
		return "organization-folder", true
	case "WorkflowMultiBranchProject":
		return "multibranch-pipeline", true
	case "WorkflowJob":
		return "pipeline", false
	case "FreeStyleProject":
		return "freestyle", false
	case "MatrixProject":
		return "matrix", false
	}

	return short, false
}

// ---- jenkins_whoami ----

type emptyInput struct{}

type whoamiOutput struct {
	client.WhoAmI
	BaseFolder string `json:"baseFolder,omitempty"`
}

func (t *Tools) whoami(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, whoamiOutput, error) {
	w, err := t.api.WhoAmI(ctx)
	if err != nil {
		return nil, whoamiOutput{}, wrap(err)
	}
	if w.Anonymous || !w.Authenticated {
		return nil, whoamiOutput{}, wrap(&client.APIError{Status: 403, Reason: "request was treated as anonymous"})
	}

	return nil, whoamiOutput{WhoAmI: *w, BaseFolder: t.api.BaseFolder()}, nil
}

// ---- jenkins_list_jobs ----

type listJobsInput struct {
	Folder string `json:"folder,omitempty" jsonschema:"Sub-folder path relative to the base folder (empty = base folder). Same format as job paths."`
	Search string `json:"search,omitempty" jsonschema:"Case-insensitive substring match on the job name."`
}

// JobItem is one row of jenkins_list_jobs.
type JobItem struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	IsFolder    bool   `json:"isFolder"`
	Status      string `json:"status,omitempty"`
	Building    bool   `json:"building"`
	Buildable   bool   `json:"buildable"`
	LastBuild   int    `json:"lastBuild,omitempty"`
	LastResult  string `json:"lastResult,omitempty"`
	LastBuildAt string `json:"lastBuildAt,omitempty"`
	URL         string `json:"url"`
}

type listJobsOutput struct {
	BaseFolder string    `json:"baseFolder,omitempty"`
	Folder     string    `json:"folder,omitempty"`
	Count      int       `json:"count"`
	Jobs       []JobItem `json:"jobs"`
}

func (t *Tools) jobItem(j client.JobSummary) JobItem {
	kind, folder := kindFromClass(j.Class)
	status, building := statusFromColor(j.Color)
	it := JobItem{
		Name: j.Name, Path: t.api.RelPath(j.FullName), Kind: kind, IsFolder: folder,
		Status: status, Building: building, Buildable: j.Buildable, URL: j.URL,
	}
	if j.LastBuild != nil {
		it.LastBuild, it.LastResult, it.LastBuildAt = j.LastBuild.Number, j.LastBuild.Result, millisToRFC3339(j.LastBuild.Timestamp)
	}

	return it
}

func (t *Tools) listJobs(ctx context.Context, _ *mcp.CallToolRequest, in listJobsInput) (*mcp.CallToolResult, listJobsOutput, error) {
	path, err := jobPath("folder", in.Folder, false)
	if err != nil {
		return nil, listJobsOutput{}, err
	}
	if err := toolkit.Text("search", in.Search, 255); err != nil {
		return nil, listJobsOutput{}, err
	}
	jobs, err := t.api.ListJobs(ctx, path)
	if err != nil {
		return nil, listJobsOutput{}, wrap(err)
	}
	q := strings.ToLower(in.Search)
	out := listJobsOutput{BaseFolder: t.api.BaseFolder(), Folder: strings.Join(path, "/"), Jobs: []JobItem{}}
	for _, j := range jobs {
		if q == "" || strings.Contains(strings.ToLower(j.Name), q) {
			out.Jobs = append(out.Jobs, t.jobItem(j))
		}
	}
	sort.Slice(out.Jobs, func(i, j int) bool { return out.Jobs[i].Name < out.Jobs[j].Name })
	out.Count = len(out.Jobs)

	return nil, out, nil
}

// ---- jenkins_get_job_info ----

type jobInput struct {
	Job string `json:"job" jsonschema:"Job path. See tool description."`
}

// Parameter is a job parameter definition.
type Parameter struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

// BuildLink is a short reference to a build.
type BuildLink struct {
	Number int    `json:"number"`
	Result string `json:"result,omitempty"`
	At     string `json:"at,omitempty"`
}

type jobOutput struct {
	Name                string      `json:"name"`
	Path                string      `json:"path"`
	Kind                string      `json:"kind"`
	IsFolder            bool        `json:"isFolder"`
	Description         string      `json:"description,omitempty"`
	Status              string      `json:"status,omitempty"`
	Building            bool        `json:"building"`
	Buildable           bool        `json:"buildable"`
	InQueue             bool        `json:"inQueue"`
	NextBuildNumber     int         `json:"nextBuildNumber,omitempty"`
	Health              []string    `json:"health,omitempty"`
	LastBuild           *BuildLink  `json:"lastBuild,omitempty"`
	LastSuccessfulBuild *BuildLink  `json:"lastSuccessfulBuild,omitempty"`
	LastFailedBuild     *BuildLink  `json:"lastFailedBuild,omitempty"`
	LastCompletedBuild  *BuildLink  `json:"lastCompletedBuild,omitempty"`
	Parameters          []Parameter `json:"parameters,omitempty"`
	Children            []JobItem   `json:"children,omitempty"`
	URL                 string      `json:"url"`
}

func link(b *client.BuildRef) *BuildLink {
	if b == nil {
		return nil
	}

	return &BuildLink{Number: b.Number, Result: b.Result, At: millisToRFC3339(b.Timestamp)}
}

func (t *Tools) getJob(ctx context.Context, _ *mcp.CallToolRequest, in jobInput) (*mcp.CallToolResult, jobOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, jobOutput{}, err
	}
	j, err := t.api.GetJob(ctx, path)
	if err != nil {
		return nil, jobOutput{}, wrap(err)
	}
	kind, folder := kindFromClass(j.Class)
	status, building := statusFromColor(j.Color)
	out := jobOutput{
		Name: j.Name, Path: t.api.RelPath(j.FullName), Kind: kind, IsFolder: folder, Description: j.Description,
		Status: status, Building: building, Buildable: j.Buildable, InQueue: j.InQueue, NextBuildNumber: j.NextBuildNumber,
		LastBuild: link(j.LastBuild), LastSuccessfulBuild: link(j.LastSuccessfulBuild),
		LastFailedBuild: link(j.LastFailedBuild), LastCompletedBuild: link(j.LastCompletedBuild), URL: j.URL,
	}
	for _, h := range j.HealthReport {
		out.Health = append(out.Health, h.Description)
	}
	for _, p := range j.Property {
		for _, d := range p.ParameterDefinitions {
			par := Parameter{Name: d.Name, Type: strings.TrimSuffix(d.Type, "ParameterDefinition"), Description: d.Description, Choices: d.Choices}
			if d.DefaultValue != nil {
				par.Default = d.DefaultValue.Value
			}
			out.Parameters = append(out.Parameters, par)
		}
	}
	for _, c := range j.Jobs {
		out.Children = append(out.Children, t.jobItem(c))
	}

	return nil, out, nil
}

// ---- jenkins_get_build_info ----

type buildInput struct {
	Job   string `json:"job" jsonschema:"Job path. See tool description."`
	Build string `json:"build,omitempty" jsonschema:"Build number, or lastBuild (default), lastSuccessfulBuild, lastFailedBuild, lastCompletedBuild."`
}

// Change is one commit in a build.
type Change struct {
	Commit  string `json:"commit"`
	Author  string `json:"author,omitempty"`
	Message string `json:"message"`
}

type buildOutput struct {
	Job             string         `json:"job"`
	Number          int            `json:"number"`
	DisplayName     string         `json:"displayName,omitempty"`
	Description     string         `json:"description,omitempty"`
	Result          string         `json:"result,omitempty"`
	Building        bool           `json:"building"`
	StartedAt       string         `json:"startedAt,omitempty"`
	DurationSeconds float64        `json:"durationSeconds"`
	EstimatedSecs   float64        `json:"estimatedDurationSeconds,omitempty"`
	Agent           string         `json:"agent,omitempty"`
	Parameters      map[string]any `json:"parameters,omitempty"`
	Causes          []string       `json:"causes,omitempty"`
	StartedBy       string         `json:"startedBy,omitempty"`
	GitCommit       string         `json:"gitCommit,omitempty"`
	GitBranches     []string       `json:"gitBranches,omitempty"`
	GitRemotes      []string       `json:"gitRemotes,omitempty"`
	Changes         []Change       `json:"changes,omitempty"`
	URL             string         `json:"url"`
}

func (t *Tools) getBuild(ctx context.Context, _ *mcp.CallToolRequest, in buildInput) (*mcp.CallToolResult, buildOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, buildOutput{}, err
	}
	ref, err := buildRef(in.Build)
	if err != nil {
		return nil, buildOutput{}, err
	}
	b, err := t.api.GetBuild(ctx, path, ref)
	if err != nil {
		return nil, buildOutput{}, wrap(err)
	}
	out := buildOutput{
		Job: strings.Join(path, "/"), Number: b.Number, DisplayName: b.DisplayName, Description: b.Description,
		Result: b.Result, Building: b.Building, StartedAt: millisToRFC3339(b.Timestamp),
		DurationSeconds: float64(b.Duration) / 1000, EstimatedSecs: float64(b.EstimatedDuration) / 1000,
		Agent: b.BuiltOn, URL: b.URL,
	}
	if b.Building {
		out.Result = "BUILDING"
	}
	for _, a := range b.Actions {
		for _, p := range a.Parameters {
			if out.Parameters == nil {
				out.Parameters = map[string]any{}
			}
			out.Parameters[p.Name] = p.Value
		}
		for _, c := range a.Causes {
			out.Causes = append(out.Causes, c.ShortDescription)
			if out.StartedBy == "" && c.UserID != "" {
				out.StartedBy = c.UserID
			}
		}
		if r := a.LastBuiltRevision; r != nil && out.GitCommit == "" {
			out.GitCommit = r.SHA1
			for _, br := range r.Branch {
				out.GitBranches = append(out.GitBranches, br.Name)
			}
			out.GitRemotes = a.RemoteURLs
		}
	}
	for _, cs := range b.ChangeSets {
		for _, it := range cs.Items {
			out.Changes = append(out.Changes, Change{Commit: it.CommitID, Author: it.Author.FullName, Message: strings.TrimSpace(it.Msg)})
		}
	}

	return nil, out, nil
}

// ---- jenkins_get_build_history ----

type historyInput struct {
	Job   string `json:"job" jsonschema:"Job path. See tool description."`
	Limit int    `json:"limit,omitempty" jsonschema:"Number of builds (default 20, max 100)."`
}

// HistoryItem is one build in the history.
type HistoryItem struct {
	Number          int     `json:"number"`
	DisplayName     string  `json:"displayName,omitempty"`
	Result          string  `json:"result,omitempty"`
	StartedAt       string  `json:"startedAt,omitempty"`
	DurationSeconds float64 `json:"durationSeconds"`
	URL             string  `json:"url"`
}

type historyOutput struct {
	Job    string        `json:"job"`
	Count  int           `json:"count"`
	Builds []HistoryItem `json:"builds"`
}

func (t *Tools) getHistory(ctx context.Context, _ *mcp.CallToolRequest, in historyInput) (*mcp.CallToolResult, historyOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, historyOutput{}, err
	}
	limit, err := toolkit.Limit("limit", in.Limit, 20, 100)
	if err != nil {
		return nil, historyOutput{}, err
	}
	builds, err := t.api.BuildHistory(ctx, path, limit)
	if err != nil {
		return nil, historyOutput{}, wrap(err)
	}
	out := historyOutput{Job: strings.Join(path, "/"), Builds: []HistoryItem{}}
	for _, b := range builds {
		r := b.Result
		if b.Building {
			r = "BUILDING"
		}
		out.Builds = append(out.Builds, HistoryItem{
			Number: b.Number, DisplayName: b.DisplayName, Result: r, StartedAt: millisToRFC3339(b.Timestamp),
			DurationSeconds: float64(b.Duration) / 1000, URL: b.URL,
		})
	}
	sort.Slice(out.Builds, func(i, j int) bool { return out.Builds[i].Number > out.Builds[j].Number })
	out.Count = len(out.Builds)

	return nil, out, nil
}

// ---- jenkins_get_console_logs ----

type consoleInput struct {
	Job      string `json:"job" jsonschema:"Job path. See tool description."`
	Build    string `json:"build,omitempty" jsonschema:"Build number, or lastBuild (default), lastFailedBuild, ..."`
	Start    *int64 `json:"start,omitempty" jsonschema:"Byte offset to read from. Omit to get the end of the log (tail)."`
	MaxBytes int    `json:"maxBytes,omitempty" jsonschema:"Maximum bytes to return (default 65536, max 1048576)."`
}

type consoleOutput struct {
	Job       string `json:"job"`
	Build     string `json:"build"`
	Start     int64  `json:"start"`
	NextStart int64  `json:"nextStart"`
	LogSize   int64  `json:"logSize"`
	MoreData  bool   `json:"moreData"`
	Truncated bool   `json:"truncated"`
	Text      string `json:"text"`
}

func (t *Tools) getConsole(ctx context.Context, _ *mcp.CallToolRequest, in consoleInput) (*mcp.CallToolResult, consoleOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, consoleOutput{}, err
	}
	ref, err := buildRef(in.Build)
	if err != nil {
		return nil, consoleOutput{}, err
	}
	maxBytes, err := toolkit.Limit("maxBytes", in.MaxBytes, defaultConsole, maxConsole)
	if err != nil {
		return nil, consoleOutput{}, err
	}
	start := int64(-1)
	if in.Start != nil {
		if *in.Start < 0 {
			return nil, consoleOutput{}, toolkit.Invalid("start must be >= 0 (omit it to read the end of the log)")
		}
		start = *in.Start
	}
	c, err := t.api.ConsoleText(ctx, path, ref, start, maxBytes)
	if err != nil {
		return nil, consoleOutput{}, wrap(err)
	}

	return nil, consoleOutput{
		Job: strings.Join(path, "/"), Build: ref, Start: c.Start, NextStart: c.NextStart, LogSize: c.Size,
		MoreData: c.MoreData, Truncated: c.Truncated, Text: c.Text,
	}, nil
}

// ---- jenkins_get_queue_info ----

// QueueEntry is one queued item.
type QueueEntry struct {
	ID          int64  `json:"id"`
	Job         string `json:"job"`
	Why         string `json:"why,omitempty"`
	Blocked     bool   `json:"blocked"`
	Stuck       bool   `json:"stuck"`
	Buildable   bool   `json:"buildable"`
	QueuedSince string `json:"queuedSince,omitempty"`
	Params      string `json:"params,omitempty"`
	URL         string `json:"url"`
}

type queueOutput struct {
	BaseFolder string       `json:"baseFolder,omitempty"`
	Count      int          `json:"count"`
	Items      []QueueEntry `json:"items"`
}

func (t *Tools) getQueue(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, queueOutput, error) {
	items, err := t.api.Queue(ctx)
	if err != nil {
		return nil, queueOutput{}, wrap(err)
	}
	out := queueOutput{BaseFolder: t.api.BaseFolder(), Items: []QueueEntry{}}
	for _, it := range items {
		out.Items = append(out.Items, QueueEntry{
			ID: it.ID, Job: it.Task.Name, Why: it.Why, Blocked: it.Blocked, Stuck: it.Stuck, Buildable: it.Buildable,
			QueuedSince: millisToRFC3339(it.InQueueSince), Params: strings.TrimSpace(it.Params), URL: it.Task.URL,
		})
	}
	out.Count = len(out.Items)

	return nil, out, nil
}
