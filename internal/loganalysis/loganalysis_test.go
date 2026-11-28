package loganalysis

import (
	"encoding/json"
	"strings"
	"testing"
)

func lines(source string, ts string, content ...string) []Line {
	out := make([]Line, len(content))
	for i, c := range content {
		out[i] = Line{Source: source, Timestamp: ts, Content: c}
	}

	return out
}

func TestParseFormats(t *testing.T) {
	es := Parse([]Line{
		{Source: "p", Content: `{"level":"warning","msg":"slow query","request_id":"req-123456","ts":1700000000.5}`},
		{Source: "p", Content: `{"level":50,"time":1700000000123,"msg":"pino error"}`},
		{Source: "p", Content: `{"log":{"level":"ERROR"},"message":"ecs nested","@timestamp":"2026-01-01T10:00:00Z"}`},
		{Source: "p", Content: `{"log.level":"debug","message":"ecs dotted key"}`},
		{Source: "p", Content: `time=2026-01-01T10:00:01Z level=info msg="logfmt line" trace_id=abcdef123`},
		{Source: "p", Content: `2026-01-01 10:00:02,123 ERROR [main] plain java line`},
		{Source: "p", Content: `[warn] bracketed`},
		{Source: "p", Content: `{"level":"info","msg": broken json`},
		{Source: "p", Content: `no level here`},
	})
	want := []struct {
		level Level
		msg   string
		json  bool
	}{
		{LevelWarn, "slow query", true}, {LevelError, "pino error", true}, {LevelError, "ecs nested", true},
		{LevelDebug, "ecs dotted key", true}, {LevelInfo, "", false}, {LevelError, "", false}, {LevelWarn, "", false},
		{LevelInfo, "", false}, {LevelUnknown, "", false},
	}
	if len(es) != len(want) {
		t.Fatalf("%d entries", len(es))
	}
	for i, w := range want {
		e := es[i]
		if e.Level != w.level || e.JSON != w.json || (w.msg != "" && e.Message != w.msg) {
			t.Errorf("#%d: %+v", i, e)
		}
	}
	if es[0].RequestID != "req-123456" || es[0].Time.UnixMilli() != 1700000000500 {
		t.Errorf("json reqid/epoch s: %+v", es[0])
	}
	if es[1].Time.UnixMilli() != 1700000000123 {
		t.Errorf("epoch ms: %v", es[1].Time)
	}
	if es[2].Time.IsZero() || es[4].RequestID != "abcdef123" || es[5].Time.IsZero() {
		t.Errorf("times/reqid: %+v %+v %+v", es[2], es[4], es[5])
	}
	if !es[7].Malformed || es[7].JSON {
		t.Errorf("malformed JSON must fall back to text: %+v", es[7])
	}
}

func TestRuntimeTimestampWins(t *testing.T) {
	es := Parse(lines("p", "2026-02-02T02:02:02.5Z", `{"msg":"x","time":"2020-01-01T00:00:00Z"}`))
	if es[0].Time.Year() != 2026 {
		t.Fatalf("runtime timestamp must win: %v", es[0].Time)
	}
}

func TestStackContinuation(t *testing.T) {
	es := Parse([]Line{
		{Source: "a", Content: `ERROR Request failed requestId=abc-123456`},
		{Source: "a", Content: `java.lang.IllegalStateException: boom`},
		{Source: "a", Content: `	at com.example.Svc.run(Svc.java:42)`},
		{Source: "a", Content: `Caused by: java.net.SocketTimeoutException: read timed out`},
		{Source: "a", Content: `	... 12 more`},
		{Source: "a", Content: `Traceback (most recent call last):`},
		{Source: "a", Content: `  File "app.py", line 3, in <module>`},
		{Source: "a", Content: `KeyError: 'user'`},
		{Source: "b", Content: `  indented but other source`},
		{Source: "a", Content: `{"msg":"json never absorbs"}`},
		{Source: "a", Content: `  indented after json`},
	})
	if len(es) != 5 {
		for _, e := range es {
			t.Logf("%q stack=%q", e.Raw, e.Stack)
		}
		t.Fatalf("got %d entries", len(es))
	}
	// Java: the un-indented exception header joins the ERROR logger line.
	if len(es[0].Stack) != 4 || es[0].Stack[0] != "java.lang.IllegalStateException: boom" {
		t.Fatalf("java stack: %q", es[0].Stack)
	}
	// Python: the final "KeyError: ..." line joins the traceback.
	if len(es[1].Stack) != 2 || es[1].Stack[1] != "KeyError: 'user'" {
		t.Fatalf("python stack: %q", es[1].Stack)
	}
}

func TestLevels(t *testing.T) {
	es := Parse(append(lines("a", "2026-01-01T00:00:00Z", "INFO ok", "INFO ok", "WARN w", "ERROR e"), lines("b", "2026-01-01T00:00:05Z", "FATAL f", "x")...))
	s := AnalyzeLevels(es)
	if s.Counts["INFO"] != 2 || s.Counts["WARN"] != 1 || s.Counts["ERROR"] != 1 || s.Counts["FATAL"] != 1 || s.Counts["UNKNOWN"] != 1 || s.Counts["DEBUG"] != 0 {
		t.Fatalf("%v", s.Counts)
	}
	if s.ErrorRate != 33.3 || s.BySource["b"]["FATAL"] != 1 || s.LastErrorMessage != "FATAL f" {
		t.Fatalf("%+v", s)
	}
}

func TestExceptions(t *testing.T) {
	es := Parse([]Line{
		{Source: "a", Timestamp: "2026-01-01T00:00:01Z", Content: `ERROR handler failed request_id=r-000001`},
		{Source: "a", Content: `java.lang.IllegalStateException: boom`},
		{Source: "a", Content: `Caused by: java.net.SocketTimeoutException: read timed out`},
		{Source: "b", Timestamp: "2026-01-01T00:00:09Z", Content: `ERROR java.lang.IllegalStateException: boom again request_id=r-000002`},
		{Source: "a", Content: `{"level":"error","msg":"db down","error":{"type":"PgConnectionError","message":"refused"},"requestId":"r-000003"}`},
		{Source: "a", Content: `ERROR could not reach host 10.0.0.1 after 3 attempts`},
		{Source: "a", Content: `ERROR could not reach host 10.0.0.2 after 5 attempts`},
		{Source: "a", Content: `INFO TypeError mentioned at info level`},
		{Source: "a", Content: `INFO all fine`},
	})
	gs := AnalyzeExceptions(es)
	byType := map[string]ExceptionGroup{}
	for _, g := range gs {
		byType[g.Type] = g
	}
	ise := byType["java.lang.IllegalStateException"]
	if ise.Count != 2 || len(ise.Sources) != 2 || ise.RootCause != "java.net.SocketTimeoutException" || ise.FirstSeen == "" || ise.LastSeen == ise.FirstSeen {
		t.Fatalf("%+v", ise)
	}
	if strings.Join(ise.RequestIDs, ",") != "r-000001,r-000002" {
		t.Fatalf("request ids %v", ise.RequestIDs)
	}
	if byType["PgConnectionError"].RequestIDs[0] != "r-000003" {
		t.Fatalf("json error.type: %+v", byType)
	}
	untyped := byType["error: ERROR could not reach host {ip} after {n} attempts"]
	if untyped.Count != 2 {
		t.Fatalf("untyped errors must group by pattern: %v", gs)
	}
	if byType["TypeError"].Levels["INFO"] != 1 {
		t.Fatalf("typed exception at INFO still detected: %+v", byType["TypeError"])
	}
	if gs[0].Count < gs[len(gs)-1].Count {
		t.Fatal("not sorted by count")
	}
}

func TestPatterns(t *testing.T) {
	es := Parse(lines("a", "",
		"INFO user 42 logged in from 10.1.2.3 session 3f2a9c1e-1b2c-4d5e-8f90-123456789abc",
		"INFO user 7 logged in from 10.9.9.9 session 9f2a9c1e-1b2c-4d5e-8f90-123456789abc",
		"INFO user 1001 logged in from 192.168.0.1 session 1f2a9c1e-1b2c-4d5e-8f90-123456789abc",
		"WARN cache miss for key 0xdeadbeef01 at 2026-01-01T10:00:00Z took 15ms",
		"WARN cache miss for key 0xcafebabe02 at 2026-01-02T11:00:00Z took 230ms",
	))
	ps := AnalyzePatterns(es)
	if len(ps) != 2 || ps[0].Count != 3 || ps[0].Pattern != "INFO user {n} logged in from {ip} session {uuid}" || ps[0].Percent != 60 {
		t.Fatalf("%+v", ps)
	}
	if ps[1].Pattern != "WARN cache miss for key {hex} at {ts} took {n}" {
		t.Fatalf("%q", ps[1].Pattern)
	}
}

func TestHTTP(t *testing.T) {
	es := Parse([]Line{
		{Source: "a", Content: `10.0.0.1 - - [01/Jan/2026:10:00:00 +0000] "GET /api/orders/123?token=s3cret HTTP/1.1" 200 512`},
		{Source: "a", Content: `10.0.0.1 - - [01/Jan/2026:10:00:01 +0000] "GET /api/orders/456 HTTP/1.1" 500 12`},
		{Source: "a", Content: `{"msg":"request","http":{"method":"post","path":"/api/pay"},"status":402,"request_id":"rq-777777"}`},
		{Source: "a", Content: `{"msg":"request","method":"GET","url":"https://svc.example.com/health?x=1","statusCode":"200"}`},
		{Source: "a", Content: `level=info method=DELETE path=/api/users/3f2a9c1e-1b2c-4d5e-8f90-123456789abc status=404`},
		{Source: "a", Content: `GET /metrics took 3ms`},
		{Source: "a", Content: `INFO nothing http here, just GETting ready`},
	})
	s, total := AnalyzeHTTP(es, 10)
	if s.TotalRequests != 6 || s.UnknownStatus != 1 || s.Failed != 3 || s.ServerErrors != 1 {
		t.Fatalf("%+v", s)
	}
	if s.StatusDistribution["200"] != 2 || s.ClassDistribution["4xx"] != 2 || s.Methods["POST"] != 1 || s.FailureRate != 60 {
		t.Fatalf("%+v", s)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "x=1") {
		t.Fatalf("query strings must not be returned: %s", b)
	}
	if total != 5 || s.Endpoints[0].Failed == 0 {
		t.Fatalf("endpoints %d %+v", total, s.Endpoints)
	}
	found := false
	for _, ep := range s.Endpoints {
		if ep.Method == "GET" && ep.Path == "/api/orders/{id}" && ep.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("path IDs must be normalized: %+v", s.Endpoints)
	}
	if s.FailedSamples[1].RequestID != "rq-777777" || s.FailedSamples[1].Status != 402 {
		t.Fatalf("%+v", s.FailedSamples)
	}
	if lim, _ := AnalyzeHTTP(es, 1); len(lim.Endpoints) != 1 || len(lim.FailedSamples) != 1 {
		t.Fatal("max not applied")
	}
}

func TestQueueURLs(t *testing.T) {
	es := Parse([]Line{
		{Source: "a", Content: `{"msg":"enqueue","queue_ingest_url":"https://sqs.eu-west-1.amazonaws.com/123456789012/ingest"}`},
		{Source: "b", Content: `{"msg":"enqueue","ctx":{"queue_ingest_url":"https://sqs.eu-west-1.amazonaws.com/123456789012/ingest"}}`},
		{Source: "a", Content: `INFO publishing queue_ingest_url=https://user:pa55@mq.example.com/q/events?X-Amz-Signature=abc&ttl=5`},
		{Source: "a", Content: `INFO publishing queue_ingest_url="https://mq.example.com/q/events"`},
		{Source: "a", Content: `INFO unrelated`},
	})
	qs := AnalyzeQueueURLs(es)
	if len(qs) != 3 || qs[0].Count != 2 || len(qs[0].Sources) != 2 {
		t.Fatalf("%+v", qs)
	}
	b, _ := json.Marshal(qs)
	if strings.Contains(string(b), "pa55") || strings.Contains(string(b), "Signature") {
		t.Fatalf("credentials or query leaked: %s", b)
	}
	if !strings.Contains(string(b), "https://mq.example.com/q/events?{query-redacted}") {
		t.Fatalf("%s", b)
	}
}

func TestSearchAndSample(t *testing.T) {
	es := Parse(lines("a", "",
		"INFO start", "INFO payment ok id=1", "WARN payment slow", "INFO between", "ERROR payment failed", "INFO end",
	))
	m, total := Search(es, SearchOptions{Keyword: "PAYMENT", Limit: 2})
	if total != 3 || len(m) != 2 || m[0].Message != "INFO payment ok id=1" {
		t.Fatalf("%d %+v", total, m)
	}
	m, total = Search(es, SearchOptions{Keyword: "payment", MinLevel: LevelWarn, Context: 1, Limit: 10})
	if total != 2 || m[1].Level != "ERROR" || m[1].Before[0] != "INFO between" || m[1].After[0] != "INFO end" {
		t.Fatalf("%d %+v", total, m)
	}
	m, total = Search(es, SearchOptions{Keyword: "payment", Offset: 2, Limit: 10})
	if total != 3 || len(m) != 1 || m[0].Level != "ERROR" {
		t.Fatalf("pagination: %+v", m)
	}
	if m, _ := Search(es, SearchOptions{Keyword: "payment", Offset: 99, Limit: 10}); len(m) != 0 {
		t.Fatal("offset past end")
	}
	m, total = Search(es, SearchOptions{Newest: true, Limit: 2})
	if total != 6 || m[0].Message != "INFO end" || m[1].Message != "ERROR payment failed" {
		t.Fatalf("sample newest first: %+v", m)
	}
}

func TestOverview(t *testing.T) {
	es := Parse([]Line{
		{Source: "b", Timestamp: "2026-01-01T10:00:05Z", Content: `{"level":"error","msg":"x"}`},
		{Source: "a", Timestamp: "2026-01-01T10:00:01Z", Content: "ERROR boom"},
		{Source: "a", Timestamp: "2026-01-01T10:00:01Z", Content: "\tat x.y(Z.java:1)"},
		{Source: "a", Timestamp: "2026-01-01T10:00:09Z", Content: `{"broken`},
	})
	o := AnalyzeOverview(es, 4)
	if o.TotalLines != 4 || o.Entries != 3 || o.JSONEntries != 1 || o.PlainEntries != 1 || o.MalformedJSON != 1 || o.Errors != 2 {
		t.Fatalf("%+v", o)
	}
	if o.Sources[0].Source != "a" || o.Sources[0].Lines != 3 || o.FirstTimestamp != "2026-01-01T10:00:01Z" || o.LastTimestamp != "2026-01-01T10:00:09Z" {
		t.Fatalf("%+v", o)
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		`Authorization: Bearer abcdefghijklmnop`:               "abcdefghijklmnop",
		`{"authorization":"Basic dXNlcjpwYXNz"}`:               "dXNlcjpwYXNz",
		`password=hunter2 user=bob`:                            "hunter2",
		`{"api_key": "k-123456", "x": 1}`:                      "k-123456",
		`token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig123456`: "eyJhbGciOiJIUzI1NiJ9",
		`aws AKIAABCDEFGHIJKLMNOP used`:                        "AKIAABCDEFGHIJKLMNOP",
		`connecting to postgres://admin:s3cr3t@db:5432/app`:    "s3cr3t",
		`GET /cb?code=abc123&state=ok`:                         "abc123",
		`Cookie: session=xyz789; theme=dark`:                   "xyz789",
	}
	for in, secret := range cases {
		out := Redact(in)
		if strings.Contains(out, secret) || !strings.Contains(out, "REDACTED") {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if Redact("user 42 logged in") != "user 42 logged in" {
		t.Error("benign text changed")
	}
	if c := Clip("ääääää", 5); c != "ää…" {
		t.Errorf("clip must cut on rune boundary: %q", c)
	}
}

func TestExceptionTypeIsRedacted(t *testing.T) {
	es := Parse(lines("a", "", "ERROR db call failed password=hunter2 token=abc123"))
	gs := AnalyzeExceptions(es)
	b, _ := json.Marshal(gs)
	if len(gs) != 1 || strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "abc123") {
		t.Fatalf("secret in exception group: %s", b)
	}
}
