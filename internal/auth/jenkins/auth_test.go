package jenkins

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func req() *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "https://jenkins.example.com/api/json", nil)

	return r
}

func TestCookieUsesConfiguredName(t *testing.T) {
	for _, pasted := range []string{"abc123", " abc123\n", "JSESSIONID.deadbeef=abc123;"} {
		r := req()
		if err := (Cookie{Name: "JSESSIONID.deadbeef", Value: StaticSecret(pasted)}).Apply(r); err != nil {
			t.Fatal(err)
		}
		c, err := r.Cookie("JSESSIONID.deadbeef")
		if err != nil || c.Value != "abc123" {
			t.Fatalf("%q: cookie %v %v", pasted, c, err)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("cookie mode must not send Authorization")
		}
	}
}

func TestCookieRejectsInjection(t *testing.T) {
	for _, v := range []string{"a; other=b", "a b", "a\r\nX-Evil: 1"} {
		if err := (Cookie{Name: "JSESSIONID.x", Value: StaticSecret(v)}).Apply(req()); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestEmptySecret(t *testing.T) {
	if err := (Cookie{Name: "JSESSIONID.x", Value: StaticSecret("")}).Apply(req()); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("got %v", err)
	}
}

func TestAPIToken(t *testing.T) {
	r := req()
	if err := (APIToken{User: "jane", Token: StaticSecret("tok")}).Apply(r); err != nil {
		t.Fatal(err)
	}
	if u, p, ok := r.BasicAuth(); !ok || u != "jane" || p != "tok" {
		t.Fatalf("%v %v %v", u, p, ok)
	}
}

func TestFileSecretRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s")
	_ = os.WriteFile(p, []byte("one\n"), 0o600)
	s := NewSecret("ignored", p)
	if v, _ := s.Value(); v != "one" {
		t.Fatal(v)
	}
	_ = os.WriteFile(p, []byte("two"), 0o600)
	if v, _ := s.Value(); v != "two" {
		t.Fatal(v)
	}
}
