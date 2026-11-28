// Package loganalysis turns raw container log lines into structured entries
// and analyses them (levels, exceptions, patterns, HTTP, queue URLs, search)
// without returning raw logs. It is pure Go (regexp, sort, encoding/json) and
// has no dependency on Jenkins, MCP or HTTP. (Shared with argocd-mcp-go; here
// "source" is the pipeline stage a line belongs to.)
package loganalysis

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Level is a normalized log level.
type Level string

// Normalized levels.
const (
	LevelDebug   Level = "DEBUG"
	LevelInfo    Level = "INFO"
	LevelWarn    Level = "WARN"
	LevelError   Level = "ERROR"
	LevelFatal   Level = "FATAL"
	LevelUnknown Level = "UNKNOWN"
)

// AllLevels in ascending severity.
var AllLevels = []Level{LevelDebug, LevelInfo, LevelWarn, LevelError, LevelFatal, LevelUnknown}

var levelRank = map[Level]int{LevelUnknown: 0, LevelDebug: 1, LevelInfo: 2, LevelWarn: 3, LevelError: 4, LevelFatal: 5}

// AtLeast reports whether l is at least min (UNKNOWN only matches UNKNOWN/empty min).
func (l Level) AtLeast(min Level) bool {
	if min == "" {
		return true
	}

	return levelRank[l] >= levelRank[min] && (l != LevelUnknown || min == LevelUnknown)
}

// ParseLevel normalizes a level name ("warning", "Err", "crit", ...).
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "debug", "dbg", "verbose", "fine", "finer", "finest":
		return LevelDebug, true
	case "info", "information", "informational", "notice":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error", "err", "severe":
		return LevelError, true
	case "fatal", "critical", "crit", "panic", "emergency", "emerg", "alert", "dpanic":
		return LevelFatal, true
	}

	return "", false
}

// Line is one raw log line as delivered by the log source.
type Line struct {
	No        int // optional line number in the original log
	Source    string
	Timestamp string // RFC3339 timestamp from the container runtime, if any
	Content   string
}

// Entry is one parsed log event (a line plus any continuation lines).
type Entry struct {
	Index     int
	LineNo    int // line number of the first line (0 = unknown)
	Source    string
	Time      time.Time
	Level     Level
	Message   string
	Raw       string
	Stack     []string
	JSON      bool
	Malformed bool // looked like JSON but failed to parse
	RequestID string
	fields    map[string]any
}

// Field returns a JSON field by key or dotted path (nil for plain-text logs).
func (e *Entry) Field(key string) (any, bool) { return lookup(e.fields, key) }

// Text is the message plus stack (for searching).
func (e *Entry) Text() string {
	if len(e.Stack) == 0 {
		return e.Raw
	}

	return e.Raw + "\n" + strings.Join(e.Stack, "\n")
}

const (
	maxLineBytes  = 256 << 10 // longer lines are not JSON-parsed
	maxStackLines = 60
)

var (
	messageKeys   = []string{"msg", "message", "@message", "log", "event", "text", "error.message"}
	levelKeys     = []string{"level", "lvl", "severity", "log.level", "levelname", "level_name", "loglevel", "@level", "status_level"}
	timeKeys      = []string{"timestamp", "time", "@timestamp", "ts", "t", "datetime", "date"}
	requestIDKeys = []string{"request_id", "requestId", "requestID", "req_id", "reqId", "x-request-id", "x_request_id", "X-Request-Id",
		"trace_id", "traceId", "traceID", "correlation_id", "correlationId", "http.request_id", "request.id", "ctx.request_id"}

	logfmtLevelRe  = regexp.MustCompile(`(?i)\b(?:level|lvl|severity)["']?\s*[:=]\s*["']?([A-Za-z]+)`)
	upperLevelRe   = regexp.MustCompile(`\b(TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|ERR|SEVERE|FATAL|CRITICAL|CRIT|PANIC)\b`)
	bracketLevelRe = regexp.MustCompile(`(?i)[\[<(](trace|debug|info|notice|warn|warning|error|fatal|critical)[\]>)]`)
	plainTimeRe    = regexp.MustCompile(`^\[?(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?)`)
	plainReqIDRe   = regexp.MustCompile(`(?i)\b(?:x-)?(?:request[_-]?id|req[_-]?id|trace[_-]?id|correlation[_-]?id)["']?\s*[:=]\s*["']?([A-Za-z0-9][A-Za-z0-9._:-]{5,})`)
	excHeaderRe    = regexp.MustCompile(`^(?:[a-z_$][\w$]*\.)+[A-Z][\w$]*(?:Exception|Error)(?::|$)`)
	pythonExcRe    = regexp.MustCompile(`^[A-Za-z_][\w.]*(?:Error|Exception|Exit|Interrupt|Warning)\b`)
)

// Parse converts raw lines into entries. Indented lines, "at ...",
// "Caused by:" and "... N more" lines (stack traces) are attached to the
// previous plain-text entry of the same source.
func Parse(lines []Line) []Entry {
	out := make([]Entry, 0, len(lines))
	for _, ln := range lines {
		content := strings.TrimRight(ln.Content, "\r\n")
		if n := len(out); n > 0 && isContinuation(content, &out[n-1], ln.Source) {
			prev := &out[n-1]
			if len(prev.Stack) < maxStackLines {
				prev.Stack = append(prev.Stack, content)
			}

			continue
		}
		e := parseLine(content)
		e.Index, e.Source, e.LineNo = len(out), ln.Source, ln.No
		if t, ok := parseTime(ln.Timestamp); ok {
			e.Time = t
		}
		out = append(out, e)
	}

	return out
}

func isContinuation(s string, prev *Entry, source string) bool {
	if prev.JSON || prev.Source != source || s == "" {
		return false
	}
	t := strings.TrimLeft(s, " \t")
	switch {
	case len(t) < len(s): // indented
		return true
	case strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "Caused by:") || strings.HasPrefix(t, "Suppressed:"):
		return true
	case strings.HasPrefix(t, "...") && strings.HasSuffix(t, "more"):
		return true
	case (prev.Level == LevelError || prev.Level == LevelFatal || prev.Level == LevelWarn) && len(prev.Stack) == 0 && excHeaderRe.MatchString(t):
		// Java: logger line followed by an un-indented "pkg.SomeException: msg" header.
		return true
	case strings.HasPrefix(prev.Message, "Traceback (most recent call last)") && pythonExcRe.MatchString(t):
		return true
	case strings.HasPrefix(prev.Message, "panic:") && (strings.HasPrefix(t, "goroutine ") || strings.HasPrefix(t, "main.") || strings.Contains(t, ".go:")):
		return true
	}

	return false
}

func parseLine(s string) Entry {
	e := Entry{Raw: s, Message: s, Level: LevelUnknown}
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "{") && len(trimmed) <= maxLineBytes {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err == nil && m != nil {
			e.JSON, e.fields = true, m
			fillFromJSON(&e)

			return e
		}
		e.Malformed = true
	}
	fillFromText(&e)

	return e
}

func fillFromJSON(e *Entry) {
	for _, k := range messageKeys {
		if v, ok := lookup(e.fields, k); ok {
			if s, ok := v.(string); ok && s != "" {
				e.Message = s

				break
			}
		}
	}
	for _, k := range levelKeys {
		v, ok := lookup(e.fields, k)
		if !ok {
			continue
		}
		if l, ok := levelOf(v); ok {
			e.Level = l

			break
		}
	}
	if e.Level == LevelUnknown {
		// The message itself may carry a level (e.g. a wrapped plain line).
		e.Level = textLevel(e.Message)
	}
	for _, k := range timeKeys {
		if v, ok := lookup(e.fields, k); ok {
			if t, ok := timeOf(v); ok {
				e.Time = t

				break
			}
		}
	}
	for _, k := range requestIDKeys {
		if v, ok := lookup(e.fields, k); ok {
			if s := scalarString(v); s != "" {
				e.RequestID = s

				break
			}
		}
	}
	if e.RequestID == "" {
		e.RequestID = textRequestID(e.Message)
	}
}

func fillFromText(e *Entry) {
	e.Level = textLevel(e.Raw)
	if strings.HasPrefix(strings.TrimSpace(e.Raw), "panic:") {
		e.Level = LevelFatal
	}
	if m := plainTimeRe.FindStringSubmatch(e.Raw); m != nil {
		if t, ok := parseTime(strings.Replace(m[1], ",", ".", 1)); ok {
			e.Time = t
		}
	}
	e.RequestID = textRequestID(e.Raw)
}

func textLevel(s string) Level {
	head := s
	if len(head) > 300 {
		head = head[:300]
	}
	if m := logfmtLevelRe.FindStringSubmatch(head); m != nil {
		if l, ok := ParseLevel(m[1]); ok {
			return l
		}
	}
	if m := bracketLevelRe.FindStringSubmatch(head); m != nil {
		if l, ok := ParseLevel(m[1]); ok {
			return l
		}
	}
	if m := upperLevelRe.FindStringSubmatch(head); m != nil {
		if l, ok := ParseLevel(m[1]); ok {
			return l
		}
	}

	return LevelUnknown
}

func textRequestID(s string) string {
	if !strings.Contains(strings.ToLower(s), "id") {
		return ""
	}
	if m := plainReqIDRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}

	return ""
}

// levelOf normalizes a JSON level value: names, or numeric pino/bunyan levels.
func levelOf(v any) (Level, bool) {
	switch x := v.(type) {
	case string:
		if l, ok := ParseLevel(x); ok {
			return l, true
		}
		if n, err := strconv.Atoi(x); err == nil {
			return numericLevel(n)
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return numericLevel(int(n))
		}
	}

	return "", false
}

func numericLevel(n int) (Level, bool) {
	switch {
	case n >= 60:
		return LevelFatal, true
	case n >= 50:
		return LevelError, true
	case n >= 40:
		return LevelWarn, true
	case n >= 30:
		return LevelInfo, true
	case n >= 10:
		return LevelDebug, true
	}

	return "", false
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999Z0700"}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}

	return time.Time{}, false
}

// timeOf parses RFC3339-like strings or epoch seconds/milliseconds/nanoseconds.
func timeOf(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		return parseTime(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		switch {
		case f > 1e17: // ns
			return time.Unix(0, int64(f)).UTC(), true
		case f > 1e11: // ms
			return time.UnixMilli(int64(f)).UTC(), true
		default: // s (with fraction)
			sec, frac := math.Modf(f)

			return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
		}
	}

	return time.Time{}, false
}

// lookup finds key in m directly ("log.level" as a literal key) or as a dotted path.
func lookup(m map[string]any, key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m[key]; ok && v != nil {
		return v, true
	}
	if !strings.Contains(key, ".") {
		return nil, false
	}
	cur := any(m)
	for _, p := range strings.Split(key, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[p]; !ok || cur == nil {
			return nil, false
		}
	}

	return cur, true
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}

	return ""
}

// findKey searches nested JSON (bounded depth) for all string values of key.
func findKey(v any, key string, depth int, out *[]string) {
	if depth > 6 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == key {
				if s := scalarString(val); s != "" {
					*out = append(*out, s)
				}
			}
			findKey(val, key, depth+1, out)
		}
	case []any:
		for _, val := range x {
			findKey(val, key, depth+1, out)
		}
	}
}
