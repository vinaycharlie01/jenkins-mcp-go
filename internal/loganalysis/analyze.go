package loganalysis

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Limits on returned text.
const (
	maxMessage = 500
	maxRaw     = 1000
	maxStack   = 12
	maxIDs     = 10
	maxSources = 20
)

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339Nano)
}

// span tracks first/last timestamps.
type span struct{ first, last time.Time }

func (s *span) add(t time.Time) {
	if t.IsZero() {
		return
	}
	if s.first.IsZero() || t.Before(s.first) {
		s.first = t
	}
	if t.After(s.last) {
		s.last = t
	}
}

// addUnique appends v to list if absent and below limit.
func addUnique(list []string, v string, limit int) []string {
	if v == "" || len(list) >= limit {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}

	return append(list, v)
}

// ---------- overview ----------

// SourceCount is lines per source.
type SourceCount struct {
	Source string `json:"source"`
	Lines  int    `json:"lines"`
}

// Overview summarizes the analysed logs.
type Overview struct {
	Sources        []SourceCount `json:"sources"`
	TotalLines     int           `json:"total_lines"`
	Entries        int           `json:"entries"`
	FirstTimestamp string        `json:"first_timestamp,omitempty"`
	LastTimestamp  string        `json:"last_timestamp,omitempty"`
	JSONEntries    int           `json:"json_entries"`
	PlainEntries   int           `json:"plain_entries"`
	MalformedJSON  int           `json:"malformed_json"`
	Errors         int           `json:"errors"`
	Warnings       int           `json:"warnings"`
}

// AnalyzeOverview counts sources, lines and formats. totalLines is the raw line
// count (before stack-trace merging).
func AnalyzeOverview(es []Entry, totalLines int) Overview {
	o := Overview{TotalLines: totalLines, Entries: len(es), Sources: []SourceCount{}}
	bySource := map[string]int{}
	var sp span
	for i := range es {
		e := &es[i]
		bySource[e.Source] += 1 + len(e.Stack)
		sp.add(e.Time)
		switch {
		case e.JSON:
			o.JSONEntries++
		case e.Malformed:
			o.MalformedJSON++
		default:
			o.PlainEntries++
		}
		switch e.Level {
		case LevelError, LevelFatal:
			o.Errors++
		case LevelWarn:
			o.Warnings++
		}
	}
	for p, n := range bySource {
		o.Sources = append(o.Sources, SourceCount{Source: p, Lines: n})
	}
	sort.Slice(o.Sources, func(i, j int) bool { return o.Sources[i].Source < o.Sources[j].Source })
	o.FirstTimestamp, o.LastTimestamp = ts(sp.first), ts(sp.last)

	return o
}

// ---------- levels ----------

// LevelStats counts levels overall and per source.
type LevelStats struct {
	Counts           map[string]int            `json:"counts"`
	Percent          map[string]float64        `json:"percent"`
	BySource         map[string]map[string]int `json:"by_source,omitempty"`
	ErrorRate        float64                   `json:"error_rate_percent"`
	LastErrorTime    string                    `json:"last_error_timestamp,omitempty"`
	LastErrorMessage string                    `json:"last_error_message,omitempty"`
}

// AnalyzeLevels counts DEBUG/INFO/WARN/ERROR/FATAL (+UNKNOWN).
func AnalyzeLevels(es []Entry) LevelStats {
	s := LevelStats{Counts: map[string]int{}, Percent: map[string]float64{}, BySource: map[string]map[string]int{}}
	for _, l := range AllLevels {
		s.Counts[string(l)] = 0
	}
	var lastErr *Entry
	for i := range es {
		e := &es[i]
		s.Counts[string(e.Level)]++
		if s.BySource[e.Source] == nil {
			s.BySource[e.Source] = map[string]int{}
		}
		s.BySource[e.Source][string(e.Level)]++
		if e.Level == LevelError || e.Level == LevelFatal {
			lastErr = e
		}
	}
	if n := len(es); n > 0 {
		for k, v := range s.Counts {
			s.Percent[k] = pct(v, n)
		}
		s.ErrorRate = pct(s.Counts[string(LevelError)]+s.Counts[string(LevelFatal)], n)
	}
	if len(s.BySource) <= 1 {
		s.BySource = nil
	}
	if lastErr != nil {
		s.LastErrorTime, s.LastErrorMessage = ts(lastErr.Time), Clip(lastErr.Message, maxMessage)
	}

	return s
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}

	return float64(int(float64(n)*1000/float64(total)+0.5)) / 10
}

// ---------- exceptions ----------

var (
	qualifiedExcRe = regexp.MustCompile(`\b((?:[a-z_$][\w$]*\.)+[A-Z][\w$]*(?:Exception|Error|Fault))\b`)
	simpleExcRe    = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*(?:Exception|Error|Fault))\b`)
	causedByRe     = regexp.MustCompile(`Caused by:\s*([\w.$]+)`)
	errorKeys      = []string{"error.type", "error.kind", "exception.type", "exception.class", "exc_type", "error_type", "errorType", "err.type", "err.name", "error.name"}
	errorTextKeys  = []string{"error", "err", "error.message", "exception", "exception.message", "exc_info", "stack", "stack_trace", "stacktrace", "error.stack", "err.stack", "error.stack_trace"}
)

// ExceptionGroup is one exception/error type and its occurrences.
type ExceptionGroup struct {
	Type        string         `json:"type"`
	Count       int            `json:"count"`
	Levels      map[string]int `json:"levels"`
	FirstSeen   string         `json:"first_seen,omitempty"`
	LastSeen    string         `json:"last_seen,omitempty"`
	Sources     []string       `json:"sources"`
	RequestIDs  []string       `json:"request_ids,omitempty"`
	RootCause   string         `json:"root_cause,omitempty"`
	Sample      string         `json:"sample_message"`
	SampleStack []string       `json:"sample_stack,omitempty"`
	span        span
}

// ExceptionType returns the exception/error type of an entry and its root
// cause, or "" when the entry is not an error.
func ExceptionType(e *Entry) (typ, root string) {
	for _, k := range errorKeys {
		if v, ok := e.Field(k); ok {
			if s := scalarString(v); s != "" {
				typ = s

				break
			}
		}
	}
	text := e.Message + "\n" + strings.Join(e.Stack, "\n")
	for _, k := range errorTextKeys {
		if v, ok := e.Field(k); ok {
			if s := scalarString(v); s != "" {
				text += "\n" + s
			} else if b, err := json.Marshal(v); err == nil && len(b) < 8192 {
				text += "\n" + string(b)
			}
		}
	}
	if typ == "" {
		if m := qualifiedExcRe.FindStringSubmatch(text); m != nil {
			typ = m[1]
		} else if m := simpleExcRe.FindStringSubmatch(text); m != nil {
			typ = m[1]
		} else if strings.HasPrefix(strings.TrimSpace(e.Message), "panic:") {
			typ = "panic"
		}
	}
	if all := causedByRe.FindAllStringSubmatch(text, -1); len(all) > 0 {
		root = strings.TrimSuffix(all[len(all)-1][1], ":")
	}
	if typ == "" && (e.Level == LevelError || e.Level == LevelFatal) {
		// Untyped error (e.g. Go, plain "ERROR ..."): group by message pattern.
		typ = "error: " + Normalize(e.Message)
	}
	// Types and causes are returned to the client: always redact and bound them.
	typ, root = Clip(typ, 160), Clip(root, 160)

	return typ, root
}

// AnalyzeExceptions groups errors by type, most frequent first.
func AnalyzeExceptions(es []Entry) []ExceptionGroup {
	groups := map[string]*ExceptionGroup{}
	for i := range es {
		e := &es[i]
		typ, root := ExceptionType(e)
		if typ == "" {
			continue
		}
		g := groups[typ]
		if g == nil {
			g = &ExceptionGroup{Type: typ, Levels: map[string]int{}, Sources: []string{}, Sample: Clip(e.Message, maxMessage)}
			for j, l := range e.Stack {
				if j >= maxStack {
					break
				}
				g.SampleStack = append(g.SampleStack, Clip(l, 300))
			}
			groups[typ] = g
		}
		g.Count++
		g.Levels[string(e.Level)]++
		g.span.add(e.Time)
		g.Sources = addUnique(g.Sources, e.Source, maxSources)
		g.RequestIDs = addUnique(g.RequestIDs, Clip(e.RequestID, 128), maxIDs)
		if root != "" {
			g.RootCause = root
		}
	}
	out := make([]ExceptionGroup, 0, len(groups))
	for _, g := range groups {
		g.FirstSeen, g.LastSeen = ts(g.span.first), ts(g.span.last)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}

		return out[i].Type < out[j].Type
	})

	return out
}

// ---------- patterns ----------

// PatternGroup is a repeated message shape.
type PatternGroup struct {
	Pattern   string  `json:"pattern"`
	Level     string  `json:"level"`
	Count     int     `json:"count"`
	Percent   float64 `json:"percent"`
	FirstSeen string  `json:"first_seen,omitempty"`
	LastSeen  string  `json:"last_seen,omitempty"`
	Sources   int     `json:"sources"`
	Sample    string  `json:"sample"`
	span      span
	sources   map[string]bool
}

// AnalyzePatterns groups messages by level + normalized message.
func AnalyzePatterns(es []Entry) []PatternGroup {
	groups := map[string]*PatternGroup{}
	for i := range es {
		e := &es[i]
		p := Normalize(e.Message)
		k := string(e.Level) + "\x00" + p
		g := groups[k]
		if g == nil {
			g = &PatternGroup{Pattern: Redact(p), Level: string(e.Level), Sample: Clip(e.Message, maxMessage), sources: map[string]bool{}}
			groups[k] = g
		}
		g.Count++
		g.span.add(e.Time)
		g.sources[e.Source] = true
	}
	out := make([]PatternGroup, 0, len(groups))
	for _, g := range groups {
		g.Percent, g.Sources = pct(g.Count, len(es)), len(g.sources)
		g.FirstSeen, g.LastSeen = ts(g.span.first), ts(g.span.last)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}

		return out[i].Pattern < out[j].Pattern
	})

	return out
}

// ---------- HTTP ----------

var (
	httpMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true}
	// "GET /path HTTP/1.1" 200  (nginx/apache/combined)
	accessLogRe = regexp.MustCompile(`"(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|CONNECT|TRACE) (\S+) HTTP/[0-9.]+" ([1-5][0-9]{2})\b`)
	// method=GET path=/x status=500 / GET /x ... status 500
	methodPathRe = regexp.MustCompile(`\b(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)\b["']?\s+["']?(/[^\s"'?]*)`)
	kvMethodRe   = regexp.MustCompile(`(?i)\bmethod["']?\s*[:=]\s*["']?(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)\b`)
	kvPathRe     = regexp.MustCompile(`(?i)\b(?:path|uri|url|route)["']?\s*[:=]\s*["']?((?:https?://[^/\s"']+)?/[^\s"'?,}]*)`)
	kvStatusRe   = regexp.MustCompile(`(?i)\b(?:status|status_code|statuscode|http_status|code)["']?\s*[:=]\s*["']?([1-5][0-9]{2})\b`)

	httpMethodKeys = []string{"method", "http.method", "http.request.method", "request.method", "req.method", "http_method", "httpMethod", "verb"}
	httpPathKeys   = []string{"path", "http.path", "http.target", "url.path", "request.path", "req.url", "request.url", "http.url", "url", "uri", "request_uri", "requestURI", "route"}
	httpStatusKeys = []string{"status", "status_code", "statusCode", "http.status_code", "http.response.status_code", "response.status", "res.statusCode", "response_code", "http_status"}
)

// Request is one extracted HTTP request.
type Request struct {
	Method string
	Path   string // without query string
	Status int    // 0 = unknown
}

// ExtractHTTP finds an HTTP request in an entry.
func ExtractHTTP(e *Entry) (Request, bool) {
	var r Request
	if e.JSON {
		for _, k := range httpMethodKeys {
			if v, ok := e.Field(k); ok {
				if s := strings.ToUpper(scalarString(v)); httpMethods[s] {
					r.Method = s

					break
				}
			}
		}
		for _, k := range httpPathKeys {
			if v, ok := e.Field(k); ok {
				if p := cleanPath(scalarString(v)); p != "" {
					r.Path = p

					break
				}
			}
		}
		for _, k := range httpStatusKeys {
			if v, ok := e.Field(k); ok {
				if n, err := strconv.Atoi(scalarString(v)); err == nil && n >= 100 && n <= 599 {
					r.Status = n

					break
				}
			}
		}
		if r.Method != "" && r.Path != "" {
			return r, true
		}
	}
	text := e.Raw
	if e.JSON {
		text = e.Message
	}
	if m := accessLogRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[3])

		return Request{Method: m[1], Path: cleanPath(m[2]), Status: n}, true
	}
	if m := kvMethodRe.FindStringSubmatch(text); m != nil {
		if p := kvPathRe.FindStringSubmatch(text); p != nil {
			r = Request{Method: strings.ToUpper(m[1]), Path: cleanPath(p[1])}
		}
	} else if m := methodPathRe.FindStringSubmatch(text); m != nil {
		r = Request{Method: m[1], Path: cleanPath(m[2])}
	}
	if r.Method == "" || r.Path == "" {
		return Request{}, false
	}
	if m := kvStatusRe.FindStringSubmatch(text); m != nil {
		r.Status, _ = strconv.Atoi(m[1])
	}

	return r, true
}

// cleanPath keeps only the path of a URL or path (query strings can hold secrets).
func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Path
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	if len(s) > 200 {
		s = s[:200]
	}

	return s
}

// Endpoint aggregates requests to one method + normalized path.
type Endpoint struct {
	Method   string         `json:"method"`
	Path     string         `json:"path"`
	Count    int            `json:"count"`
	Failed   int            `json:"failed"`
	Statuses map[string]int `json:"statuses"`
}

// FailedRequest is one request with status >= 400.
type FailedRequest struct {
	Time      string `json:"timestamp,omitempty"`
	Source    string `json:"source"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id,omitempty"`
	Message   string `json:"message"`
}

// HTTPStats is the HTTP analysis.
type HTTPStats struct {
	TotalRequests      int             `json:"total_requests"`
	UnknownStatus      int             `json:"unknown_status"`
	StatusDistribution map[string]int  `json:"status_distribution"`
	ClassDistribution  map[string]int  `json:"class_distribution"`
	Methods            map[string]int  `json:"methods"`
	Failed             int             `json:"failed_requests"`
	ServerErrors       int             `json:"server_errors"`
	FailureRate        float64         `json:"failure_rate_percent"`
	Endpoints          []Endpoint      `json:"endpoints"`
	FailedSamples      []FailedRequest `json:"failed_samples"`
}

// AnalyzeHTTP extracts requests; endpoints and failed samples are limited to max.
func AnalyzeHTTP(es []Entry, max int) (HTTPStats, int) {
	s := HTTPStats{StatusDistribution: map[string]int{}, ClassDistribution: map[string]int{}, Methods: map[string]int{}, Endpoints: []Endpoint{}, FailedSamples: []FailedRequest{}}
	eps := map[string]*Endpoint{}
	known := 0
	for i := range es {
		e := &es[i]
		r, ok := ExtractHTTP(e)
		if !ok {
			continue
		}
		s.TotalRequests++
		s.Methods[r.Method]++
		np := NormalizePath(r.Path)
		k := r.Method + " " + np
		ep := eps[k]
		if ep == nil {
			ep = &Endpoint{Method: r.Method, Path: Redact(np), Statuses: map[string]int{}}
			eps[k] = ep
		}
		ep.Count++
		if r.Status == 0 {
			s.UnknownStatus++

			continue
		}
		known++
		code := strconv.Itoa(r.Status)
		s.StatusDistribution[code]++
		s.ClassDistribution[fmt.Sprintf("%dxx", r.Status/100)]++
		ep.Statuses[code]++
		if r.Status >= 400 {
			s.Failed++
			ep.Failed++
			if r.Status >= 500 {
				s.ServerErrors++
			}
			if len(s.FailedSamples) < max {
				s.FailedSamples = append(s.FailedSamples, FailedRequest{Time: ts(e.Time), Source: e.Source, Method: r.Method,
					Path: Redact(r.Path), Status: r.Status, RequestID: Clip(e.RequestID, 128), Message: Clip(e.Message, 300)})
			}
		}
	}
	s.FailureRate = pct(s.Failed, known)
	for _, ep := range eps {
		s.Endpoints = append(s.Endpoints, *ep)
	}
	sort.Slice(s.Endpoints, func(i, j int) bool {
		a, b := s.Endpoints[i], s.Endpoints[j]
		if a.Failed != b.Failed {
			return a.Failed > b.Failed
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}

		return a.Method+a.Path < b.Method+b.Path
	})
	total := len(s.Endpoints)
	if total > max {
		s.Endpoints = s.Endpoints[:max]
	}

	return s, total
}

// ---------- queue URLs ----------

// QueueKey is the field analysed by the queue_urls mode.
const QueueKey = "queue_ingest_url"

var queueTextRe = regexp.MustCompile(`queue_ingest_url["']?\s*[:=]\s*["']?([^\s"',}\]]+)`)

// QueueURL is one unique queue URL.
type QueueURL struct {
	URL       string   `json:"url"`
	Count     int      `json:"count"`
	FirstSeen string   `json:"first_seen,omitempty"`
	LastSeen  string   `json:"last_seen,omitempty"`
	Sources   []string `json:"sources"`
	span      span
}

// SanitizeURL drops credentials and the query string (which may carry
// signatures or tokens), keeping scheme, host and path.
func SanitizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return Clip(raw, 300)
	}
	u.User = nil
	q := u.RawQuery != ""
	u.RawQuery, u.Fragment = "", ""
	s := u.String()
	if q {
		s += "?{query-redacted}"
	}

	return s
}

// AnalyzeQueueURLs counts unique queue_ingest_url values.
func AnalyzeQueueURLs(es []Entry) []QueueURL {
	idx := map[string]*QueueURL{}
	for i := range es {
		e := &es[i]
		var vals []string
		if e.JSON {
			findKey(e.fields, QueueKey, 0, &vals)
		}
		if len(vals) == 0 {
			for _, m := range queueTextRe.FindAllStringSubmatch(e.Message, -1) {
				vals = append(vals, m[1])
			}
			if e.JSON && len(vals) == 0 {
				for _, m := range queueTextRe.FindAllStringSubmatch(e.Raw, -1) {
					vals = append(vals, m[1])
				}
			}
		}
		seen := map[string]bool{}
		for _, v := range vals {
			u := SanitizeURL(v)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			q := idx[u]
			if q == nil {
				q = &QueueURL{URL: u, Sources: []string{}}
				idx[u] = q
			}
			q.Count++
			q.span.add(e.Time)
			q.Sources = addUnique(q.Sources, e.Source, maxSources)
		}
	}
	out := make([]QueueURL, 0, len(idx))
	for _, q := range idx {
		q.FirstSeen, q.LastSeen = ts(q.span.first), ts(q.span.last)
		out = append(out, *q)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}

		return out[i].URL < out[j].URL
	})

	return out
}

// ---------- search / sample ----------

// Match is one returned log entry.
type Match struct {
	Index     int      `json:"index"`
	LineNo    int      `json:"line,omitempty"`
	Time      string   `json:"timestamp,omitempty"`
	Source    string   `json:"source"`
	Level     string   `json:"level"`
	RequestID string   `json:"request_id,omitempty"`
	Message   string   `json:"message"`
	Stack     []string `json:"stack,omitempty"`
	Before    []string `json:"context_before,omitempty"`
	After     []string `json:"context_after,omitempty"`
}

// SearchOptions controls Search and Sample.
type SearchOptions struct {
	Keyword  string // case-insensitive substring; empty = all
	MinLevel Level  // "" = any
	Context  int    // entries before/after each match (same source)
	Offset   int
	Limit    int
	Newest   bool // newest first (Sample)
}

func toMatch(e *Entry, raw bool) Match {
	m := Match{Index: e.Index, LineNo: e.LineNo, Time: ts(e.Time), Source: e.Source, Level: string(e.Level), RequestID: Clip(e.RequestID, 128)}
	if raw {
		m.Message = Clip(e.Raw, maxRaw)
	} else {
		m.Message = Clip(e.Message, maxRaw)
	}
	for j, l := range e.Stack {
		if j >= maxStack {
			break
		}
		m.Stack = append(m.Stack, Clip(l, 300))
	}

	return m
}

// Search returns one page of matching entries and the total number of matches.
func Search(es []Entry, o SearchOptions) ([]Match, int) {
	kw := strings.ToLower(o.Keyword)
	var hits []int
	for i := range es {
		e := &es[i]
		if !e.Level.AtLeast(o.MinLevel) {
			continue
		}
		if kw != "" && !strings.Contains(strings.ToLower(e.Text()), kw) {
			continue
		}
		hits = append(hits, i)
	}
	if o.Newest {
		sort.SliceStable(hits, func(a, b int) bool { return hits[a] > hits[b] })
	}
	total := len(hits)
	if o.Offset >= total {
		return []Match{}, total
	}
	end := min(o.Offset+o.Limit, total)
	out := make([]Match, 0, end-o.Offset)
	for _, i := range hits[o.Offset:end] {
		m := toMatch(&es[i], o.Newest)
		if o.Context > 0 {
			for j := max(0, i-o.Context); j < i; j++ {
				if es[j].Source == es[i].Source {
					m.Before = append(m.Before, Clip(es[j].Raw, 300))
				}
			}
			for j := i + 1; j <= min(len(es)-1, i+o.Context); j++ {
				if es[j].Source == es[i].Source {
					m.After = append(m.After, Clip(es[j].Raw, 300))
				}
			}
		}
		out = append(out, m)
	}

	return out, total
}

// TimeRange is the span of the analysed entries.
func TimeRange(es []Entry) (string, string) {
	var sp span
	for i := range es {
		sp.add(es[i].Time)
	}

	return ts(sp.first), ts(sp.last)
}
