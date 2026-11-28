package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestSecretsRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "debug", "json")
	log.Info("x", "auth_token", "s3cret-1", "Cookie", "s3cret-2", "jenkins_session", "s3cret-3", "server", "https://a.example.com")
	out := buf.String()
	if strings.Contains(out, "s3cret") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "https://a.example.com") || strings.Count(out, redacted) != 3 {
		t.Fatalf("unexpected output: %s", out)
	}
}
