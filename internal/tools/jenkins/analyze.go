package jenkins

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vinaycharlie01/jenkins-mcp-go/internal/consolelog"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/loganalysis"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

// Build-log analysis modes.
const (
	ModeOverview   = "overview"
	ModeErrors     = "errors"
	ModeStages     = "stages"
	ModeTests      = "tests"
	ModeExceptions = "exceptions"
	ModePatterns   = "patterns"
	ModeSearch     = "search"
	ModeSample     = "sample"
	ModeAll        = "all"
)

var buildLogModes = map[string]bool{ModeOverview: true, ModeErrors: true, ModeStages: true, ModeTests: true,
	ModeExceptions: true, ModePatterns: true, ModeSearch: true, ModeSample: true, ModeAll: true}

const (
	defaultScanBytesAnalyze = 8 << 20
	maxScanBytesAnalyze     = 32 << 20
	defaultAnalyzeResults   = 20
	maxAnalyzeResults       = 200
	maxAnalyzeOffset        = 100000
	maxAnalyzeContext       = 5
)

type analyzeInput struct {
	Job          string `json:"job" jsonschema:"Job path relative to the base folder, e.g. my-service/main."`
	Build        string `json:"build,omitempty" jsonschema:"Build number, or lastBuild (default), lastFailedBuild, ..."`
	AnalysisType string `json:"analysis_type,omitempty" jsonschema:"overview (default: result, stages, likely cause), errors, stages, tests, exceptions, patterns, search, sample (raw lines), or all."`
	Stage        string `json:"stage,omitempty" jsonschema:"Only analyse lines of this Pipeline stage or parallel branch (case-insensitive)."`
	Search       string `json:"search,omitempty" jsonschema:"Keyword for analysis_type=search (case-insensitive)."`
	Level        string `json:"level,omitempty" jsonschema:"Minimum level for search/sample: DEBUG, INFO, WARN, ERROR or FATAL."`
	ContextLines int    `json:"context_lines,omitempty" jsonschema:"For search: lines of context before/after each match (0-5)."`
	MaxResults   int    `json:"max_results,omitempty" jsonschema:"Maximum items per result list (default 20, max 200)."`
	Offset       int    `json:"offset,omitempty" jsonschema:"Pagination offset (use next_offset from the previous call)."`
	ScanBytes    int    `json:"scan_bytes,omitempty" jsonschema:"Bytes from the END of the console log to analyse (default 8 MiB, max 32 MiB)."`
}

type buildLogSummary struct {
	LogSize      int64    `json:"log_size_bytes"`
	BytesScanned int64    `json:"bytes_scanned"`
	PartialLog   bool     `json:"partial_log"`
	Lines        int      `json:"lines"`
	Stages       int      `json:"stages"`
	Commands     int      `json:"commands"`
	ErrorLines   int      `json:"error_lines"`
	FailedStage  string   `json:"failed_stage,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

type testsResult struct {
	Totals *consolelog.TestTotals  `json:"totals,omitempty"`
	Failed []consolelog.FailedTest `json:"failed"`
}

type buildLogResults struct {
	LikelyCause *consolelog.Cause            `json:"likely_cause,omitempty"`
	Errors      *consolelog.ErrorSummary     `json:"errors,omitempty"`
	Stages      []consolelog.Stage           `json:"stages,omitempty"`
	Tests       *testsResult                 `json:"tests,omitempty"`
	Exceptions  []loganalysis.ExceptionGroup `json:"exceptions,omitempty"`
	Patterns    []loganalysis.PatternGroup   `json:"patterns,omitempty"`
	Matches     []loganalysis.Match          `json:"matches,omitempty"`
}

type buildLogTimeRange struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type analyzeBuildOutput struct {
	Job          string            `json:"job"`
	Build        int               `json:"build"`
	Result       string            `json:"result,omitempty"`
	AnalysisType string            `json:"analysis_type"`
	Stage        string            `json:"stage,omitempty"`
	TimeRange    buildLogTimeRange `json:"time_range"`
	Summary      buildLogSummary   `json:"summary"`
	Results      buildLogResults   `json:"results"`
	TotalMatches int               `json:"total_matches"`
	Truncated    bool              `json:"truncated"`
	NextOffset   *int              `json:"next_offset,omitempty"`
}

func (t *Tools) registerAnalyzeBuildLogs(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "jenkins_analyze_build_logs",
		Title: "Jenkins: analyse a build's console log",
		Description: "Analyse a build's full console log (the last scan_bytes, default 8 MiB) and return compact structured results instead of raw logs. " +
			"overview: result, stages, failing stage and the LIKELY ROOT CAUSE line with context. errors: CI errors grouped and ranked " +
			"(credentials, permission, network, resources, git, docker, dependency, compile, pipeline_script, test, quality, timeout, build_tool, aborted). " +
			"stages: per-stage lines, duration, errors, exit code, last command. tests: failed tests (Go, JUnit/Maven, pytest, Jest, Gradle, .NET) and totals. " +
			"exceptions, patterns, search (keyword/level + context), sample (raw tail lines, only when needed), all. " +
			"Optional stage filter. Secrets are redacted; results are bounded by max_results and paged with offset/next_offset.",
		Annotations: readOnly,
	}, t.analyzeBuildLogs)
}

func (in *analyzeInput) validate() (loganalysis.Level, error) {
	if in.AnalysisType == "" {
		in.AnalysisType = ModeOverview
	}
	in.AnalysisType = strings.ToLower(in.AnalysisType)
	if !buildLogModes[in.AnalysisType] {
		return "", toolkit.Invalid("analysis_type must be one of overview, errors, stages, tests, exceptions, patterns, search, sample, all")
	}
	if err := toolkit.Text("stage", in.Stage, 255); err != nil {
		return "", err
	}
	if err := toolkit.Text("search", in.Search, 256); err != nil {
		return "", err
	}
	var level loganalysis.Level
	if in.Level != "" {
		l, ok := loganalysis.ParseLevel(in.Level)
		if !ok {
			return "", toolkit.Invalid("level must be DEBUG, INFO, WARN, ERROR or FATAL")
		}
		level = l
	}
	if in.AnalysisType == ModeSearch && strings.TrimSpace(in.Search) == "" && level == "" {
		return "", toolkit.Invalid("analysis_type=search needs search and/or level")
	}
	var err error
	if in.MaxResults, err = toolkit.Limit("max_results", in.MaxResults, defaultAnalyzeResults, maxAnalyzeResults); err != nil {
		return "", err
	}
	if in.ScanBytes, err = toolkit.Limit("scan_bytes", in.ScanBytes, defaultScanBytesAnalyze, maxScanBytesAnalyze); err != nil {
		return "", err
	}
	if in.ContextLines < 0 || in.ContextLines > maxAnalyzeContext {
		return "", toolkit.Invalid("context_lines must be between 0 and %d", maxAnalyzeContext)
	}
	if in.Offset < 0 || in.Offset > maxAnalyzeOffset {
		return "", toolkit.Invalid("offset must be between 0 and %d", maxAnalyzeOffset)
	}

	return level, nil
}

func pageOf[T any](items []T, offset, limit int) ([]T, *int) {
	if offset >= len(items) {
		return nil, nil
	}
	end := min(offset+limit, len(items))
	if end < len(items) {
		return items[offset:end], &end
	}

	return items[offset:end], nil
}

// filterStage keeps only the lines (and stage entry) of one stage.
func filterStage(p consolelog.Parsed, stage string) (consolelog.Parsed, string, error) {
	var name string
	for _, s := range p.Stages {
		if strings.EqualFold(s.Name, stage) {
			name = s.Name
			p.Stages = []consolelog.Stage{s}

			break
		}
	}
	if name == "" {
		names := make([]string, 0, len(p.Stages))
		for _, s := range p.Stages {
			names = append(names, s.Name)
		}

		return p, "", &toolkit.ToolError{Code: toolkit.CodeNotFound,
			Message: fmt.Sprintf("stage %q not found in the scanned log; stages: %s", stage, strings.Join(names, ", "))}
	}
	lines := p.Lines[:0:0]
	for _, l := range p.Lines {
		if l.Stage == name {
			lines = append(lines, l)
		}
	}
	p.Lines = lines

	return p, name, nil
}

func (t *Tools) analyzeBuildLogs(ctx context.Context, _ *mcp.CallToolRequest, in analyzeInput) (*mcp.CallToolResult, analyzeBuildOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, analyzeBuildOutput{}, err
	}
	ref, err := buildRef(in.Build)
	if err != nil {
		return nil, analyzeBuildOutput{}, err
	}
	level, err := in.validate()
	if err != nil {
		return nil, analyzeBuildOutput{}, err
	}

	// Pin the build, then read the end of its console (existing client calls).
	b, err := t.api.GetBuild(ctx, path, ref)
	if err != nil {
		return nil, analyzeBuildOutput{}, wrap(err)
	}
	num := fmt.Sprint(b.Number)
	con, err := t.api.ConsoleText(ctx, path, num, -1, in.ScanBytes)
	if err != nil {
		return nil, analyzeBuildOutput{}, wrap(err)
	}

	out := analyzeBuildOutput{Job: strings.Join(path, "/"), Build: b.Number, Result: b.Result, AnalysisType: in.AnalysisType}
	if b.Building {
		out.Result = "BUILDING"
	}
	parsed := consolelog.Parse(con.Text)
	if in.Stage != "" {
		if parsed, out.Stage, err = filterStage(parsed, strings.TrimSpace(in.Stage)); err != nil {
			return nil, analyzeBuildOutput{}, err
		}
	}
	errs := consolelog.AnalyzeErrors(parsed)
	out.Summary = buildLogSummary{LogSize: con.Size, BytesScanned: int64(len(con.Text)), PartialLog: con.Start > 0,
		Lines: len(parsed.Lines), Stages: len(parsed.Stages), Commands: parsed.Commands, ErrorLines: errs.ErrorLines, FailedStage: errs.FailedStage}
	if con.Start > 0 {
		out.Summary.Notes = append(out.Summary.Notes, fmt.Sprintf("analysed the last %d of %d bytes; earlier stages may be missing - raise scan_bytes (max %d MiB) if needed",
			len(con.Text), con.Size, maxScanBytesAnalyze>>20))
	}
	if b.Building || con.MoreData {
		out.Summary.Notes = append(out.Summary.Notes, "the build is still running; the log is incomplete")
	}

	raw := make([]loganalysis.Line, len(parsed.Lines))
	for i, l := range parsed.Lines {
		raw[i] = loganalysis.Line{No: l.No, Source: l.Stage, Timestamp: l.Time, Content: l.Text}
	}
	es := loganalysis.Parse(raw)
	out.TimeRange.From, out.TimeRange.To = loganalysis.TimeRange(es)

	mode, all := in.AnalysisType, in.AnalysisType == ModeAll
	n, off := in.MaxResults, in.Offset
	var next *int
	more := false
	setNext := func(nx *int) {
		if nx != nil {
			more = true
			if next == nil {
				next = nx
			}
		}
	}

	if mode == ModeOverview || all {
		out.Results.LikelyCause = errs.LikelyCause
		out.TotalMatches = len(es)
		if errs.LikelyCause == nil && (b.Result == "FAILURE" || b.Result == "UNSTABLE") {
			out.Summary.Notes = append(out.Summary.Notes, "no recognizable error line found; try analysis_type=search with level=ERROR or a keyword")
		}
		if !all {
			out.Results.Stages, _ = pageOf(parsed.Stages, 0, n)
		}
	}
	if mode == ModeErrors || all {
		e := errs
		var nx *int
		e.Groups, nx = pageOf(errs.Groups, off, n)
		if e.Groups == nil {
			e.Groups = []consolelog.ErrorGroup{}
		}
		out.Results.Errors = &e
		if !all {
			out.TotalMatches = len(errs.Groups)
		}
		setNext(nx)
	}
	if mode == ModeStages || all {
		var nx *int
		out.Results.Stages, nx = pageOf(parsed.Stages, off, n)
		if !all {
			out.TotalMatches = len(parsed.Stages)
		}
		setNext(nx)
	}
	if mode == ModeTests || all {
		failed, totals := consolelog.FailedTests(parsed.Lines)
		page, nx := pageOf(failed, off, n)
		if page == nil {
			page = []consolelog.FailedTest{}
		}
		out.Results.Tests = &testsResult{Totals: totals, Failed: page}
		if !all {
			out.TotalMatches = len(failed)
		}
		setNext(nx)
	}
	if mode == ModeExceptions || all {
		g := loganalysis.AnalyzeExceptions(es)
		var nx *int
		out.Results.Exceptions, nx = pageOf(g, off, n)
		if !all {
			out.TotalMatches = len(g)
		}
		setNext(nx)
	}
	if mode == ModePatterns || all {
		g := loganalysis.AnalyzePatterns(es)
		var nx *int
		out.Results.Patterns, nx = pageOf(g, off, n)
		if !all {
			out.TotalMatches = len(g)
		}
		setNext(nx)
	}
	if mode == ModeSearch || (all && (in.Search != "" || level != "")) {
		m, total := loganalysis.Search(es, loganalysis.SearchOptions{Keyword: in.Search, MinLevel: level, Context: in.ContextLines, Offset: off, Limit: n})
		out.Results.Matches = m
		if !all {
			out.TotalMatches = total
		}
		if off+len(m) < total {
			end := off + len(m)
			setNext(&end)
		}
	}
	if mode == ModeSample {
		m, total := loganalysis.Search(es, loganalysis.SearchOptions{MinLevel: level, Offset: off, Limit: n, Newest: true})
		out.Results.Matches, out.TotalMatches = m, total
		if off+len(m) < total {
			end := off + len(m)
			setNext(&end)
		}
		out.Summary.Notes = append(out.Summary.Notes, "sample returns redacted raw lines, newest first; prefer the analysis modes for large logs")
	}

	out.NextOffset = next
	out.Truncated = more

	return nil, out, nil
}
