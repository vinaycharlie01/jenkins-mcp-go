package jenkins

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
)

const (
	maxFilesPerCommit = 20
	maxCompareBuilds  = 20
)

// Commit is one SCM change.
type Commit struct {
	Build   int      `json:"build,omitempty"`
	Commit  string   `json:"commit"`
	Author  string   `json:"author,omitempty"`
	Time    string   `json:"time,omitempty"`
	Message string   `json:"message"`
	Files   []string `json:"files,omitempty"`
	More    int      `json:"moreFiles,omitempty"`
}

func commitsOf(b *client.Build) []Commit {
	sets := b.ChangeSets
	if b.ChangeSet != nil {
		sets = append(sets, *b.ChangeSet)
	}
	var out []Commit
	for _, cs := range sets {
		for _, it := range cs.Items {
			c := Commit{Build: b.Number, Commit: it.CommitID, Author: it.Author.FullName, Time: millisToRFC3339(it.Timestamp), Message: trim(it.Msg, 1000)}
			c.Files = it.AffectedPaths
			if len(c.Files) > maxFilesPerCommit {
				c.More = len(c.Files) - maxFilesPerCommit
				c.Files = c.Files[:maxFilesPerCommit]
			}
			out = append(out, c)
		}
	}

	return out
}

type gitInfo struct {
	Commit   string   `json:"commit,omitempty"`
	Branches []string `json:"branches,omitempty"`
	Remotes  []string `json:"remotes,omitempty"`
}

func gitOf(b *client.Build) gitInfo {
	var g gitInfo
	for _, a := range b.Actions {
		if r := a.LastBuiltRevision; r != nil && g.Commit == "" {
			g.Commit = r.SHA1
			for _, br := range r.Branch {
				g.Branches = append(g.Branches, br.Name)
			}
			g.Remotes = a.RemoteURLs
		}
	}

	return g
}

// ---- jenkins_get_build_changes ----

type changesOutput struct {
	Job      string   `json:"job"`
	Build    int      `json:"build"`
	Result   string   `json:"result,omitempty"`
	Git      gitInfo  `json:"git"`
	Culprits []string `json:"culprits,omitempty"`
	Count    int      `json:"count"`
	Commits  []Commit `json:"commits"`
	Notes    []string `json:"notes,omitempty"`
}

func (t *Tools) buildChanges(ctx context.Context, _ *mcp.CallToolRequest, in buildRefInput) (*mcp.CallToolResult, changesOutput, error) {
	path, n, result, err := t.resolve(ctx, in.Job, in.Build)
	if err != nil {
		return nil, changesOutput{}, err
	}
	b, err := t.api.GetBuild(ctx, path, fmt.Sprint(n))
	if err != nil {
		return nil, changesOutput{}, wrap(err)
	}
	out := changesOutput{Job: strings.Join(path, "/"), Build: n, Result: result, Git: gitOf(b), Commits: []Commit{}}
	out.Commits = append(out.Commits, commitsOf(b)...)
	for i := range out.Commits {
		out.Commits[i].Build = 0
	}
	for _, c := range b.Culprits {
		out.Culprits = append(out.Culprits, c.FullName)
	}
	out.Count = len(out.Commits)
	if out.Count == 0 {
		out.Notes = append(out.Notes, "no new commits in this build (rebuild, timer/parameter trigger, or first build)")
	}

	return nil, out, nil
}

// ---- jenkins_compare_builds ----

type compareInput struct {
	Job    string `json:"job" jsonschema:"Job path relative to the base folder."`
	Base   string `json:"base,omitempty" jsonschema:"Baseline build (number or permalink). Default lastSuccessfulBuild."`
	Target string `json:"target,omitempty" jsonschema:"Build to compare (number or permalink). Default lastBuild."`
}

// BuildSide summarizes one side of a comparison.
type BuildSide struct {
	Build           int               `json:"build"`
	Result          string            `json:"result,omitempty"`
	StartedAt       string            `json:"startedAt,omitempty"`
	DurationSeconds float64           `json:"durationSeconds"`
	Agent           string            `json:"agent,omitempty"`
	Git             gitInfo           `json:"git"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	Causes          []string          `json:"causes,omitempty"`
	Tests           *TestCounts       `json:"tests,omitempty"`
}

// TestCounts are JUnit totals.
type TestCounts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// StageDiff compares one stage.
type StageDiff struct {
	Stage         string  `json:"stage"`
	BaseStatus    string  `json:"baseStatus,omitempty"`
	TargetStatus  string  `json:"targetStatus,omitempty"`
	BaseSeconds   float64 `json:"baseSeconds"`
	TargetSeconds float64 `json:"targetSeconds"`
	StatusChanged bool    `json:"statusChanged"`
	OnlyIn        string  `json:"onlyIn,omitempty"` // "base" or "target" when the stage exists in one build only
}

// ParamDiff is one changed parameter.
type ParamDiff struct {
	Name   string `json:"name"`
	Base   string `json:"base"`
	Target string `json:"target"`
}

type compareOutput struct {
	Job              string      `json:"job"`
	Base             BuildSide   `json:"base"`
	Target           BuildSide   `json:"target"`
	ResultChanged    bool        `json:"resultChanged"`
	DurationDelta    float64     `json:"durationDeltaSeconds"`
	GitChanged       bool        `json:"gitCommitChanged"`
	AgentChanged     bool        `json:"agentChanged"`
	ParameterChanges []ParamDiff `json:"parameterChanges"`
	Stages           []StageDiff `json:"stages,omitempty"`
	FirstDiffStage   string      `json:"firstStageWithDifferentStatus,omitempty"`
	CommitsBetween   []Commit    `json:"commitsBetween"`
	Notes            []string    `json:"notes,omitempty"`
}

func (t *Tools) side(ctx context.Context, path []string, ref string) (*client.Build, BuildSide, error) {
	n, _, _, err := t.api.ResolveBuild(ctx, path, ref)
	if err != nil {
		return nil, BuildSide{}, err
	}
	b, err := t.api.GetBuild(ctx, path, fmt.Sprint(n))
	if err != nil {
		return nil, BuildSide{}, err
	}
	s := BuildSide{Build: b.Number, Result: b.Result, StartedAt: millisToRFC3339(b.Timestamp),
		DurationSeconds: float64(b.Duration) / 1000, Agent: b.BuiltOn, Git: gitOf(b), Parameters: map[string]string{}}
	if b.Building {
		s.Result = "BUILDING"
	}
	for _, a := range b.Actions {
		for _, p := range a.Parameters {
			s.Parameters[p.Name] = fmt.Sprint(p.Value)
		}
		for _, c := range a.Causes {
			s.Causes = append(s.Causes, c.ShortDescription)
		}
	}

	return b, s, nil
}

func (t *Tools) compareBuilds(ctx context.Context, _ *mcp.CallToolRequest, in compareInput) (*mcp.CallToolResult, compareOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, compareOutput{}, err
	}
	if in.Base == "" {
		in.Base = "lastSuccessfulBuild"
	}
	baseRef, err := buildRef(in.Base)
	if err != nil {
		return nil, compareOutput{}, err
	}
	targetRef, err := buildRef(in.Target)
	if err != nil {
		return nil, compareOutput{}, err
	}
	_, base, err := t.side(ctx, path, baseRef)
	if err != nil {
		return nil, compareOutput{}, wrap(err)
	}
	_, target, err := t.side(ctx, path, targetRef)
	if err != nil {
		return nil, compareOutput{}, wrap(err)
	}
	out := compareOutput{Job: strings.Join(path, "/"), Base: base, Target: target, ParameterChanges: []ParamDiff{}, CommitsBetween: []Commit{}}
	if base.Build == target.Build {
		out.Notes = append(out.Notes, "base and target are the same build")
	}
	out.ResultChanged = base.Result != target.Result
	out.DurationDelta = float64(int((target.DurationSeconds-base.DurationSeconds)*10)) / 10
	out.GitChanged = base.Git.Commit != target.Git.Commit
	out.AgentChanged = base.Agent != target.Agent

	names := map[string]bool{}
	for k := range base.Parameters {
		names[k] = true
	}
	for k := range target.Parameters {
		names[k] = true
	}
	for k := range names {
		bv, bok := base.Parameters[k]
		tv, tok := target.Parameters[k]
		if bv != tv || bok != tok {
			out.ParameterChanges = append(out.ParameterChanges, ParamDiff{Name: k, Base: bv, Target: tv})
		}
	}
	sort.Slice(out.ParameterChanges, func(i, j int) bool { return out.ParameterChanges[i].Name < out.ParameterChanges[j].Name })

	// Stages (Pipeline jobs only; best effort).
	br, berr := t.api.DescribeRun(ctx, path, base.Build, false)
	tr, terr := t.api.DescribeRun(ctx, path, target.Build, false)
	if berr == nil && terr == nil {
		out.Stages, out.FirstDiffStage = diffStages(br, tr)
	} else if !errors.Is(berr, client.ErrNotFound) && berr != nil {
		out.Notes = append(out.Notes, "stages unavailable: "+berr.Error())
	}

	// Tests (best effort).
	for _, s := range []*BuildSide{&out.Base, &out.Target} {
		if r, err := t.api.TestReport(ctx, path, s.Build, client.TestTreeCounts); err == nil {
			s.Tests = &TestCounts{Passed: r.PassCount, Failed: r.FailCount, Skipped: r.SkipCount}
		}
	}
	// Commits that came in after base, up to and including target.
	if target.Build > base.Build {
		from := base.Build + 1
		if target.Build-base.Build > maxCompareBuilds {
			from = target.Build - maxCompareBuilds + 1
			out.Notes = append(out.Notes, fmt.Sprintf("commits shown for the last %d builds only", maxCompareBuilds))
		}
		for n := from; n <= target.Build; n++ {
			b, err := t.api.GetBuild(ctx, path, fmt.Sprint(n))
			if err != nil {
				if errors.Is(err, client.ErrNotFound) {
					continue // deleted build
				}

				return nil, compareOutput{}, wrap(err)
			}
			out.CommitsBetween = append(out.CommitsBetween, commitsOf(b)...)
		}
	} else if target.Build < base.Build {
		out.Notes = append(out.Notes, "target is older than base; commitsBetween is empty")
	}

	return nil, out, nil
}

func diffStages(base, target *client.PipelineRun) ([]StageDiff, string) {
	idx := map[string]client.Stage{}
	for _, s := range base.Stages {
		idx[s.Name] = s
	}
	seen := map[string]bool{}
	var out []StageDiff
	first := ""
	for _, s := range target.Stages {
		d := StageDiff{Stage: s.Name, TargetStatus: s.Status, TargetSeconds: secs(s.DurationMillis)}
		if b, ok := idx[s.Name]; ok {
			d.BaseStatus, d.BaseSeconds = b.Status, secs(b.DurationMillis)
			d.StatusChanged = d.BaseStatus != d.TargetStatus
			if d.StatusChanged && first == "" {
				first = s.Name
			}
		} else {
			d.OnlyIn = "target"
		}
		seen[s.Name] = true
		out = append(out, d)
	}
	for _, s := range base.Stages {
		if !seen[s.Name] {
			out = append(out, StageDiff{Stage: s.Name, BaseStatus: s.Status, BaseSeconds: secs(s.DurationMillis), OnlyIn: "base"})
		}
	}

	return out, first
}
