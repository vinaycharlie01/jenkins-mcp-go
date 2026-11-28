// Package jenkins is a read-only client for the Jenkins REST API.
//
// Every job path is relative to an optional base folder (JENKINS_BASE_FOLDER),
// and every path segment is escaped, so requests cannot leave that folder.
package jenkins

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	authjenkins "github.com/vinaycharlie01/jenkins-mcp-go/internal/auth/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/pkg/version"
)

const maxJSONBytes = 32 << 20

// Sentinel errors; match with errors.Is.
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not found")
)

// APIError is a non-2xx response from Jenkins.
type APIError struct {
	Status int
	// Reason is a safe, human-readable explanation (never HTML or credentials).
	Reason string
}

func (e *APIError) Error() string {
	switch {
	case e.Is(ErrUnauthorized):
		return "Jenkins did not accept the credential (" + e.Reason + "): the session is missing or expired. Sign in to Jenkins in the browser and refresh JENKINS_SESSION"
	case e.Status == http.StatusForbidden:
		return "permission denied by Jenkins (403): " + e.Reason
	case e.Status == http.StatusNotFound:
		return "not found (404): " + e.Reason
	default:
		return fmt.Sprintf("Jenkins returned HTTP %d: %s", e.Status, e.Reason)
	}
}

// Is maps responses to the sentinel errors.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized || (e.Status >= 300 && e.Status < 400) ||
			(e.Status == http.StatusForbidden && strings.Contains(e.Reason, "anonymous"))
	case ErrForbidden:
		return e.Status == http.StatusForbidden && !strings.Contains(e.Reason, "anonymous")
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	}

	return false
}

// Client talks to one Jenkins controller as the authenticated user.
type Client struct {
	base   *url.URL
	folder []string
	http   *http.Client
	auth   authjenkins.Authenticator
}

// Options configures the client.
type Options struct {
	BaseFolder         []string
	CAFile             string
	InsecureSkipVerify bool
	Timeout            time.Duration
	HTTPClient         *http.Client // tests
}

// New returns a client for baseURL.
func New(baseURL string, auth authjenkins.Authenticator, opts Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid Jenkins URL %q", baseURL)
	}
	if auth == nil {
		return nil, errors.New("authenticator is required")
	}
	hc := opts.HTTPClient
	if hc == nil {
		if hc, err = newHTTPClient(opts); err != nil {
			return nil, err
		}
	}

	return &Client{base: u, folder: opts.BaseFolder, http: hc, auth: auth}, nil
}

func newHTTPClient(opts Options) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read JENKINS_CA_FILE: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates in %s", opts.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	tlsCfg.InsecureSkipVerify = opts.InsecureSkipVerify //nolint:gosec // explicit operator opt-in
	tr.TLSClientConfig = tlsCfg
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		// Never follow redirects: an SSO redirect means the session expired,
		// and following one could send the cookie elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// BaseFolder returns the configured base folder path ("" for root).
func (c *Client) BaseFolder() string { return strings.Join(c.folder, "/") }

// jobURL returns the URL of the job at path (relative to the base folder),
// followed by extra raw path segments (e.g. "42", "api", "json").
func (c *Client) jobURL(path []string, extra ...string) (*url.URL, error) {
	u := *c.base
	raw := strings.TrimRight(u.EscapedPath(), "/")
	plain := strings.TrimRight(u.Path, "/")
	for _, name := range append(append([]string{}, c.folder...), path...) {
		if name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("invalid job name %q", name)
		}
		raw += "/job/" + url.PathEscape(name)
		plain += "/job/" + name
	}
	for _, s := range extra {
		raw += "/" + url.PathEscape(s)
		plain += "/" + s
	}
	u.Path, u.RawPath = plain+"/", raw+"/"
	if len(extra) > 0 {
		u.Path, u.RawPath = plain, raw
	}

	return &u, nil
}

// send performs an authenticated GET and returns a 2xx response (caller closes).
func (c *Client) send(ctx context.Context, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "jenkins-mcp-go/"+version.Version)
	if err := c.auth.Apply(req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Jenkins: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	return nil, &APIError{Status: resp.StatusCode, Reason: reason(resp)}
}

// reason builds a safe explanation from Jenkins' diagnostic headers, never
// from the (HTML) body.
func reason(resp *http.Response) string {
	who := resp.Header.Get("X-You-Are-Authenticated-As")
	perm := resp.Header.Get("X-Required-Permission")
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return "redirected to login"
	case resp.StatusCode == http.StatusForbidden && who == "anonymous":
		return "request was treated as anonymous"
	case resp.StatusCode == http.StatusForbidden:
		r := "user " + who
		if perm != "" {
			r += " lacks " + perm
		}

		return r
	case resp.StatusCode == http.StatusNotFound:
		return "job or build does not exist, or you lack permission to see it"
	}

	return http.StatusText(resp.StatusCode)
}

// getJSON GETs u with a ?tree= projection and decodes the JSON response.
func (c *Client) getJSON(ctx context.Context, u *url.URL, tree string, out any) error {
	if tree != "" {
		q := u.Query()
		q.Set("tree", tree)
		u.RawQuery = q.Encode()
	}
	resp, err := c.send(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return fmt.Errorf("unexpected %q response from Jenkins (an SSO login page? refresh JENKINS_SESSION)", ct)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode Jenkins response: %w", err)
	}

	return nil
}

// relPath converts a Jenkins fullName to a path relative to the base folder.
func (c *Client) relPath(fullName string) string {
	prefix := c.BaseFolder()
	if prefix == "" {
		return fullName
	}

	return strings.TrimPrefix(fullName, prefix+"/")
}
