package jenkins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	authjenkins "github.com/vinaycharlie01/jenkins-mcp-go/internal/auth/jenkins"
)

const (
	cookieName = "JSESSIONID.test1234"
	session    = "sess-secret-value"
)

func newClient(t *testing.T, folder []string, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	hc := srv.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c, err := New(srv.URL, authjenkins.Cookie{Name: cookieName, Value: authjenkins.StaticSecret(session)}, Options{BaseFolder: folder, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}

	return c, srv
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func TestCookieSentAndBaseFolderScoping(t *testing.T) {
	var path, tree string
	c, _ := newClient(t, []string{"TeamA"}, func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(cookieName)
		if err != nil || ck.Value != session {
			t.Errorf("cookie: %v %v", ck, err)
		}
		path, tree = r.URL.EscapedPath(), r.URL.Query().Get("tree")
		writeJSON(w, `{"jobs":[{"_class":"com.cloudbees.hudson.plugins.folder.Folder","name":"svc","fullName":"TeamA/svc","url":"u","buildable":false}]}`)
	})
	jobs, err := c.ListJobs(context.Background(), nil)
	if err != nil || len(jobs) != 1 || c.RelPath(jobs[0].FullName) != "svc" {
		t.Fatalf("%v %+v", err, jobs)
	}
	if path != "/job/TeamA/api/json" || !strings.HasPrefix(tree, "jobs[") {
		t.Fatalf("path %s tree %s", path, tree)
	}
	_, _ = c.ListJobs(context.Background(), []string{"svc", "feature%2Fx"})
	if path != "/job/TeamA/job/svc/job/feature%252Fx/api/json" {
		t.Fatalf("nested/escaped path %s", path)
	}
	_, _ = c.ListJobs(context.Background(), []string{"a b", "x/y"})
	if path != "/job/TeamA/job/a%20b/job/x%2Fy/api/json" {
		t.Fatalf("escaping %s", path)
	}
}

func TestTraversalRejected(t *testing.T) {
	c, _ := newClient(t, []string{"TeamA"}, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	for _, p := range [][]string{{".."}, {"svc", "."}, {""}} {
		if _, err := c.GetJob(context.Background(), p); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
	if _, err := c.GetBuild(context.Background(), []string{"svc"}, "../../script"); err == nil {
		t.Error("bad build ref accepted")
	}
}

func TestRootWithoutFolder(t *testing.T) {
	var path string
	c, _ := newClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		writeJSON(w, `{"jobs":[]}`)
	})
	if _, err := c.ListJobs(context.Background(), nil); err != nil || path != "/api/json" {
		t.Fatalf("%v %s", err, path)
	}
}

func TestJobBuildHistoryParsing(t *testing.T) {
	var gotTree string
	c, _ := newClient(t, []string{"TeamA"}, func(w http.ResponseWriter, r *http.Request) {
		gotTree = r.URL.Query().Get("tree")
		switch r.URL.EscapedPath() {
		case "/job/TeamA/job/svc/api/json":
			if strings.HasPrefix(gotTree, "builds[") {
				writeJSON(w, `{"builds":[{"number":12,"result":"SUCCESS","timestamp":1,"duration":5,"url":"u"},{"number":11,"building":true,"url":"u"}]}`)

				return
			}
			writeJSON(w, `{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"svc","fullName":"TeamA/svc","url":"u",
				"buildable":true,"color":"red","lastBuild":{"number":12,"url":"u"},
				"property":[{},{"parameterDefinitions":[{"name":"ENV","type":"ChoiceParameterDefinition","choices":["dev","qa"],"defaultParameterValue":{"value":"dev"}}]}]}`)
		case "/job/TeamA/job/svc/12/api/json":
			writeJSON(w, `{"number":12,"result":"FAILURE","building":false,"duration":1000,"timestamp":1,"url":"u",
				"actions":[{"_class":"hudson.model.ParametersAction","parameters":[{"name":"ENV","value":"qa"}]},
				{"causes":[{"shortDescription":"Started by user Jane","userId":"jane"}]},{}]}`)
		default:
			t.Errorf("unexpected %s", r.URL.EscapedPath())
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	j, err := c.GetJob(ctx, []string{"svc"})
	if err != nil || j.Color != "red" || j.Property[1].ParameterDefinitions[0].Choices[1] != "qa" {
		t.Fatalf("%v %+v", err, j)
	}
	b, err := c.GetBuild(ctx, []string{"svc"}, "12")
	if err != nil || b.Result != "FAILURE" || b.Actions[0].Parameters[0].Value != "qa" || b.Actions[1].Causes[0].UserID != "jane" {
		t.Fatalf("%v %+v", err, b)
	}
	h, err := c.BuildHistory(ctx, []string{"svc"}, 5)
	if err != nil || len(h) != 2 || !h[1].Building || !strings.HasSuffix(gotTree, "{0,5}") {
		t.Fatalf("%v %+v %s", err, h, gotTree)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		target  error
	}{
		{"anonymous 403 = expired session", 403, map[string]string{"X-You-Are-Authenticated-As": "anonymous"}, ErrUnauthorized},
		{"401", 401, nil, ErrUnauthorized},
		{"SSO redirect", 302, map[string]string{"Location": "https://login.example.com/authorize"}, ErrUnauthorized},
		{"real 403", 403, map[string]string{"X-You-Are-Authenticated-As": "jane", "X-Required-Permission": "hudson.model.Item.Read"}, ErrForbidden},
		{"404", 404, nil, ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("<html>secret page body</html>"))
			})
			_, err := c.GetJob(context.Background(), []string{"svc"})
			if !errors.Is(err, tc.target) {
				t.Fatalf("got %v", err)
			}
			if msg := err.Error(); strings.Contains(msg, "<html>") || strings.Contains(msg, session) {
				t.Fatalf("leaky error: %s", msg)
			}
		})
	}
	// The forbidden message names the missing permission.
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-You-Are-Authenticated-As", "jane")
		w.Header().Set("X-Required-Permission", "hudson.model.Item.Read")
		w.WriteHeader(403)
	})
	if _, err := c.GetJob(context.Background(), []string{"x"}); !strings.Contains(err.Error(), "jane lacks hudson.model.Item.Read") {
		t.Fatal(err)
	}
}

func TestHTMLLoginPageDetected(t *testing.T) {
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>login</html>"))
	})
	if _, err := c.ListJobs(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "JENKINS_SESSION") {
		t.Fatalf("got %v", err)
	}
}

// consoleServer serves log through progressiveText like Jenkins (start past
// the end restarts from 0).
func consoleServer(t *testing.T, log string, building bool, starts *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/job/TeamA/job/svc/7/logText/progressiveText" {
			t.Errorf("path %s", r.URL.EscapedPath())
		}
		s := r.URL.Query().Get("start")
		*starts = append(*starts, s)
		start, _ := strconv.Atoi(s)
		if start > len(log) {
			start = 0
		}
		w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
		w.Header().Set("X-Text-Size", strconv.Itoa(len(log)))
		if building {
			w.Header().Set("X-More-Data", "true")
		}
		_, _ = w.Write([]byte(log[start:]))
	}
}

func TestConsolePagingAndTail(t *testing.T) {
	log := "line1\nline2\nline3\nERROR at the end\n"
	var starts []string
	c, _ := newClient(t, []string{"TeamA"}, consoleServer(t, log, false, &starts))
	ctx := context.Background()

	con, err := c.ConsoleText(ctx, []string{"svc"}, "7", 0, 12)
	if err != nil || con.Text != "line1\nline2\n" || !con.Truncated || !con.MoreData || con.NextStart != 12 {
		t.Fatalf("%v %+v", err, con)
	}
	con, err = c.ConsoleText(ctx, []string{"svc"}, "7", con.NextStart, 1000)
	if err != nil || con.Text != "line3\nERROR at the end\n" || con.Truncated || con.MoreData || con.NextStart != int64(len(log)) {
		t.Fatalf("%v %+v", err, con)
	}

	starts = nil
	con, err = c.ConsoleText(ctx, []string{"svc"}, "7", -1, 17)
	if err != nil || con.Text != "ERROR at the end\n" || con.Start != int64(len(log)-17) {
		t.Fatalf("tail: %v %+v", err, con)
	}
	if strings.Join(starts, ",") != "0,"+strconv.Itoa(len(log)-17) {
		t.Fatalf("tail requests %v", starts)
	}
}

func TestConsoleDoesNotSplitRunes(t *testing.T) {
	var starts []string
	c, _ := newClient(t, []string{"TeamA"}, consoleServer(t, "ab€cd", true, &starts)) // € is 3 bytes
	con, err := c.ConsoleText(context.Background(), []string{"svc"}, "7", 0, 3)
	if err != nil || con.Text != "ab" || con.NextStart != 2 {
		t.Fatalf("%v %+v", err, con)
	}
}

func TestQueueFilteredToBaseFolder(t *testing.T) {
	var srvURL string
	c, srv := newClient(t, []string{"TeamA"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue/api/json" {
			t.Errorf("path %s", r.URL.Path)
		}
		writeJSON(w, `{"items":[
			{"id":1,"why":"Waiting","task":{"name":"svc","url":"`+srvURL+`/job/TeamA/job/svc/"}},
			{"id":2,"why":"Waiting","task":{"name":"other","url":"`+srvURL+`/job/OtherTeam/job/x/"}},
			{"id":3,"why":"Waiting","task":{"name":"prefix-trick","url":"`+srvURL+`/job/TeamAEvil/job/x/"}}]}`)
	})
	srvURL = srv.URL
	items, err := c.Queue(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != 1 {
		t.Fatalf("%v %+v", err, items)
	}
}

func TestWhoAmI(t *testing.T) {
	c, _ := newClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/whoAmI/api/json" {
			t.Errorf("path %s", r.URL.Path)
		}
		writeJSON(w, `{"name":"jane@example.com","anonymous":false,"authenticated":true,"authorities":["authenticated","devs"]}`)
	})
	who, err := c.WhoAmI(context.Background())
	if err != nil || who.Name != "jane@example.com" || who.Anonymous {
		t.Fatalf("%v %+v", err, who)
	}
}
