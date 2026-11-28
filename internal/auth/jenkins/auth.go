// Package jenkins authenticates outgoing Jenkins requests.
//
// Today: the user's own browser SSO session cookie (local, single-user use).
// Also: username + Jenkins API token. Another approved mechanism can be added
// by implementing Authenticator; the client does not change.
package jenkins

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ErrNoCredential means the secret is not configured or is empty.
var ErrNoCredential = errors.New("no Jenkins credential configured: set JENKINS_SESSION (or JENKINS_SESSION_FILE) or JENKINS_API_TOKEN")

// Authenticator applies a credential to an outgoing request.
type Authenticator interface {
	Apply(r *http.Request) error
}

// Secret returns the current value of a secret.
type Secret interface {
	Value() (string, error)
}

// StaticSecret is a value fixed at startup (from an environment variable).
type StaticSecret string

// Value implements Secret.
func (s StaticSecret) Value() (string, error) {
	v := strings.TrimSpace(string(s))
	if v == "" {
		return "", ErrNoCredential
	}

	return v, nil
}

// FileSecret re-reads the secret on every call so it can be rotated without a restart.
type FileSecret struct{ Path string }

// Value implements Secret.
func (f FileSecret) Value() (string, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return "", fmt.Errorf("read Jenkins secret file: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", ErrNoCredential
	}

	return v, nil
}

// NewSecret prefers the file when a path is given.
func NewSecret(value, file string) Secret {
	if file != "" {
		return FileSecret{Path: file}
	}

	return StaticSecret(value)
}

// Cookie sends the Jenkins session cookie (name is configurable because the
// JSESSIONID suffix differs per instance).
type Cookie struct {
	Name  string
	Value Secret
}

// Apply implements Authenticator.
func (c Cookie) Apply(r *http.Request) error {
	v, err := c.Value.Value()
	if err != nil {
		return err
	}
	// Tolerate a pasted "NAME=value" or "NAME=value;".
	v = strings.TrimSuffix(strings.TrimPrefix(v, c.Name+"="), ";")
	if v == "" || strings.ContainsAny(v, "; \t\r\n\",\\") {
		return errors.New("JENKINS_SESSION contains characters not allowed in a cookie value; paste only the cookie value")
	}
	r.AddCookie(&http.Cookie{Name: c.Name, Value: v})

	return nil
}

// APIToken uses HTTP basic auth with a user's Jenkins API token. API-token
// requests are exempt from CSRF crumbs, which matters once mutations are added.
type APIToken struct {
	User  string
	Token Secret
}

// Apply implements Authenticator.
func (a APIToken) Apply(r *http.Request) error {
	t, err := a.Token.Value()
	if err != nil {
		return err
	}
	r.SetBasicAuth(a.User, t)

	return nil
}
