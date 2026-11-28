package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func jenkinsEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"JENKINS_URL":         "https://jenkins.example.com/",
		"JENKINS_COOKIE_NAME": "JSESSIONID.1a2b3c4d",
		"JENKINS_SESSION":     "s",
	}
	for k, v := range extra {
		m[k] = v
	}

	return m
}

func TestLoad(t *testing.T) {
	c, err := LoadFrom(env(jenkinsEnv(map[string]string{"JENKINS_BASE_FOLDER": "/TeamA/sub/"})))
	if err != nil {
		t.Fatal(err)
	}
	j := c.Jenkins
	if j.BaseURL != "https://jenkins.example.com" || j.AuthMode != JenkinsAuthCookie || j.Timeout.Seconds() != 30 {
		t.Fatalf("%+v", j)
	}
	if strings.Join(j.BaseFolder, "|") != "TeamA|sub" {
		t.Fatalf("folder %v", j.BaseFolder)
	}
	if c, err := LoadFrom(env(jenkinsEnv(map[string]string{"JENKINS_URL": "jenkins.example.com"}))); err != nil || c.Jenkins.BaseURL != "https://jenkins.example.com" {
		t.Fatalf("bare host: %v %+v", err, c)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"missing url":         {jenkinsEnv(map[string]string{"JENKINS_URL": ""}), "JENKINS_URL is required"},
		"missing cookie name": {jenkinsEnv(map[string]string{"JENKINS_COOKIE_NAME": ""}), "JENKINS_COOKIE_NAME"},
		"bad cookie name":     {jenkinsEnv(map[string]string{"JENKINS_COOKIE_NAME": "JSESSIONID x;"}), "JENKINS_COOKIE_NAME"},
		"missing session":     {jenkinsEnv(map[string]string{"JENKINS_SESSION": ""}), "JENKINS_SESSION"},
		"traversal folder":    {jenkinsEnv(map[string]string{"JENKINS_BASE_FOLDER": "TeamA/../Other"}), "JENKINS_BASE_FOLDER"},
		"http url":            {jenkinsEnv(map[string]string{"JENKINS_URL": "http://jenkins.example.com"}), "https"},
		"creds in url":        {jenkinsEnv(map[string]string{"JENKINS_URL": "https://u:p@jenkins.example.com"}), "credentials"},
		"bad mode":            {jenkinsEnv(map[string]string{"JENKINS_AUTH_MODE": "oauth"}), "JENKINS_AUTH_MODE"},
		"api-token no user":   {jenkinsEnv(map[string]string{"JENKINS_AUTH_MODE": "api-token", "JENKINS_API_TOKEN": "t"}), "JENKINS_USER"},
		"bad bool":            {jenkinsEnv(map[string]string{"JENKINS_INSECURE": "maybe"}), "JENKINS_INSECURE"},
		"bad timeout":         {jenkinsEnv(map[string]string{"JENKINS_REQUEST_TIMEOUT": "soon"}), "JENKINS_REQUEST_TIMEOUT"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFrom(env(tc.env)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := LoadFrom(env(jenkinsEnv(map[string]string{"JENKINS_SESSION": "", "JENKINS_SESSION_FILE": "/tmp/s"}))); err != nil {
		t.Fatalf("session file alone should be valid: %v", err)
	}
	if _, err := LoadFrom(env(jenkinsEnv(map[string]string{"JENKINS_AUTH_MODE": "api-token", "JENKINS_USER": "jane", "JENKINS_API_TOKEN": "t"}))); err != nil {
		t.Fatalf("api-token mode: %v", err)
	}
}

func TestSplitJobPath(t *testing.T) {
	ok := map[string]string{
		"":                  "",
		"TeamA":             "TeamA",
		"/TeamA/svc/":       "TeamA|svc",
		"job/TeamA/job/svc": "TeamA|svc",
		"svc/feature%2Fx":   "svc|feature%2Fx",
		"job":               "job",
		"Team A/svc":        "Team A|svc",
	}
	for in, want := range ok {
		got, err := SplitJobPath(in)
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("%q => %v %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a//b", "a/../b", "..", "./a", "a/\x00b"} {
		if _, err := SplitJobPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestImagePattern(t *testing.T) {
	c, err := LoadFrom(env(jenkinsEnv(map[string]string{"JENKINS_IMAGE_PATTERN": `IMG=(?P<image>\S+)`})))
	if err != nil || c.Jenkins.ImagePattern == nil {
		t.Fatalf("%v", err)
	}
	for _, bad := range []string{`(?P<image>[`, `no-groups`} {
		if _, err := LoadFrom(env(jenkinsEnv(map[string]string{"JENKINS_IMAGE_PATTERN": bad}))); err == nil || !strings.Contains(err.Error(), "JENKINS_IMAGE_PATTERN") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
