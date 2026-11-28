// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Jenkins auth modes.
const (
	JenkinsAuthCookie   = "cookie"    // browser SSO session cookie (local, single user)
	JenkinsAuthAPIToken = "api-token" // username + Jenkins API token
)

// Config is the full server configuration.
type Config struct {
	Jenkins Jenkins
	Logging Logging
}

// Jenkins configures the Jenkins connection.
type Jenkins struct {
	BaseURL  string // JENKINS_URL, normalised https URL
	AuthMode string // JENKINS_AUTH_MODE: cookie (default) | api-token
	// Cookie mode. The cookie name differs per Jenkins instance
	// (JSESSIONID.<suffix>), so it is configuration, never code.
	CookieName  string // JENKINS_COOKIE_NAME
	Session     string // JENKINS_SESSION (secret)
	SessionFile string // JENKINS_SESSION_FILE, re-read per request
	// API-token mode.
	User      string // JENKINS_USER
	APIToken  string // JENKINS_API_TOKEN (secret)
	TokenFile string // JENKINS_API_TOKEN_FILE, re-read per request
	// BaseFolder scopes every tool to this folder (e.g. "Team" or
	// "Team/sub"). Empty means the Jenkins root.
	BaseFolder         []string      // JENKINS_BASE_FOLDER
	CAFile             string        // JENKINS_CA_FILE
	InsecureSkipVerify bool          // JENKINS_INSECURE
	Timeout            time.Duration // JENKINS_REQUEST_TIMEOUT
}

// Logging configures structured logging.
type Logging struct {
	Level  string // LOG_LEVEL
	Format string // LOG_FORMAT: json | text
}

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration through getenv (injectable for tests).
func LoadFrom(getenv func(string) string) (*Config, error) {
	r := reader{getenv: getenv}
	cfg := &Config{
		Logging: Logging{Level: r.str("LOG_LEVEL", "info"), Format: r.str("LOG_FORMAT", "json")},
	}
	j := &cfg.Jenkins

	rawURL := r.str("JENKINS_URL", "")
	if rawURL == "" {
		r.errs = append(r.errs, errors.New("JENKINS_URL is required"))
	} else {
		base, err := NormalizeURL(rawURL)
		if err != nil {
			r.errs = append(r.errs, fmt.Errorf("JENKINS_URL: %w", err))
		}
		j.BaseURL = base
	}
	j.AuthMode = strings.ToLower(r.str("JENKINS_AUTH_MODE", JenkinsAuthCookie))
	j.CookieName = r.str("JENKINS_COOKIE_NAME", "")
	j.Session = strings.TrimSpace(getenv("JENKINS_SESSION"))
	j.SessionFile = r.str("JENKINS_SESSION_FILE", "")
	j.User = r.str("JENKINS_USER", "")
	j.APIToken = strings.TrimSpace(getenv("JENKINS_API_TOKEN"))
	j.TokenFile = r.str("JENKINS_API_TOKEN_FILE", "")
	j.CAFile = r.str("JENKINS_CA_FILE", "")
	j.InsecureSkipVerify = r.boolean("JENKINS_INSECURE", false)
	j.Timeout = r.duration("JENKINS_REQUEST_TIMEOUT", 30*time.Second)

	folder, err := SplitJobPath(r.str("JENKINS_BASE_FOLDER", ""))
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("JENKINS_BASE_FOLDER: %w", err))
	}
	j.BaseFolder = folder

	switch j.AuthMode {
	case JenkinsAuthCookie:
		if !cookieNameRe.MatchString(j.CookieName) {
			r.errs = append(r.errs, errors.New("JENKINS_COOKIE_NAME is required in cookie mode and must be a valid cookie name (e.g. JSESSIONID.1a2b3c4d)"))
		}
		if j.Session == "" && j.SessionFile == "" {
			r.errs = append(r.errs, errors.New("JENKINS_SESSION or JENKINS_SESSION_FILE is required in cookie mode"))
		}
	case JenkinsAuthAPIToken:
		if j.User == "" || (j.APIToken == "" && j.TokenFile == "") {
			r.errs = append(r.errs, errors.New("JENKINS_USER and JENKINS_API_TOKEN (or JENKINS_API_TOKEN_FILE) are required in api-token mode"))
		}
	default:
		r.errs = append(r.errs, fmt.Errorf("JENKINS_AUTH_MODE must be %q or %q", JenkinsAuthCookie, JenkinsAuthAPIToken))
	}
	if j.Timeout <= 0 {
		r.errs = append(r.errs, errors.New("JENKINS_REQUEST_TIMEOUT must be positive"))
	}

	return cfg, errors.Join(r.errs...)
}

// cookieNameRe is the RFC 6265 cookie-name token charset.
var cookieNameRe = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// SplitJobPath splits a slash-separated Jenkins job path ("a/b/c", also
// accepting the URL form "job/a/job/b") into job names. Empty, "." and ".."
// segments are rejected so a path can never escape its folder.
func SplitJobPath(p string) ([]string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return nil, nil
	}
	parts := strings.Split(p, "/")
	// Accept the URL form job/a/job/b.
	if len(parts)%2 == 0 {
		urlForm := true
		for i := 0; i < len(parts); i += 2 {
			if parts[i] != "job" {
				urlForm = false

				break
			}
		}
		if urlForm {
			names := make([]string, 0, len(parts)/2)
			for i := 1; i < len(parts); i += 2 {
				names = append(names, parts[i])
			}
			parts = names
		}
	}
	for _, s := range parts {
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("invalid job path %q: empty, '.' or '..' segment", p)
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				return nil, fmt.Errorf("invalid job path %q: control character", p)
			}
		}
	}

	return parts, nil
}

// NormalizeURL accepts a host or a full URL and returns an https base URL.
// Plain http is refused so credentials never travel unencrypted.
func NormalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", s)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("URL %q must use https", s)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("URL %q must not contain credentials, query or fragment", s)
	}

	return strings.TrimRight(u.String(), "/"), nil
}

type reader struct {
	getenv func(string) string
	errs   []error
}

func (r *reader) str(key, def string) string {
	if v := strings.TrimSpace(r.getenv(key)); v != "" {
		return v
	}

	return def
}

func (r *reader) boolean(key string, def bool) bool {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
	}

	return b
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
	}

	return d
}
