// Package toolkit holds helpers shared by all integrations' MCP tools:
// a consistent error envelope and input validation.
package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Error codes returned in the error envelope.
const (
	CodeInvalidInput  = "invalid_input"
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeNotFound      = "not_found"
	CodeNotConfigured = "not_configured"
	CodeTimeout       = "timeout"
	CodeUpstream      = "upstream_error"
)

// ToolError is the error every tool returns. Its message is a JSON envelope
// {"error":{"code":"...","message":"..."}} so clients can parse it.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ToolError) Error() string {
	b, _ := json.Marshal(map[string]*ToolError{"error": e})

	return string(b)
}

// Invalid returns an invalid_input error.
func Invalid(format string, args ...any) error {
	return &ToolError{Code: CodeInvalidInput, Message: fmt.Sprintf(format, args...)}
}

// Classifier maps integration-specific errors to a code ("" = not matched).
type Classifier func(error) string

// Wrap converts err into a ToolError using classify, falling back to timeout
// or upstream_error.
func Wrap(err error, classify Classifier) error {
	if err == nil {
		return nil
	}
	var te *ToolError
	if errors.As(err, &te) {
		return te
	}
	code := ""
	if classify != nil {
		code = classify(err)
	}
	if code == "" {
		if errors.Is(err, context.DeadlineExceeded) {
			code = CodeTimeout
		} else {
			code = CodeUpstream
		}
	}

	return &ToolError{Code: code, Message: err.Error()}
}

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// K8sName validates a required DNS-1123 subdomain (e.g. an Application name).
func K8sName(field, v string) error {
	if v == "" {
		return Invalid("%s is required", field)
	}
	if len(v) > 253 || !dnsSubdomain.MatchString(v) {
		return Invalid("%s %q is not a valid Kubernetes name (lowercase letters, digits, '-' and '.')", field, v)
	}

	return nil
}

// OptionalK8sName validates an optional DNS-1123 subdomain.
func OptionalK8sName(field, v string) error {
	if v == "" {
		return nil
	}

	return K8sName(field, v)
}

// OptionalNamespace validates an optional DNS-1123 label.
func OptionalNamespace(field, v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 63 || !dnsLabel.MatchString(v) {
		return Invalid("%s %q is not a valid Kubernetes namespace", field, v)
	}

	return nil
}

// Text validates free text: bounded length, no control characters.
func Text(field, v string, maxLen int) error {
	if len(v) > maxLen {
		return Invalid("%s is longer than %d characters", field, maxLen)
	}
	if strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return Invalid("%s contains control characters", field)
	}

	return nil
}

// Limit applies a default and validates 1..maxV.
func Limit(field string, v, def, maxV int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < 0 || v > maxV {
		return 0, Invalid("%s must be between 1 and %d", field, maxV)
	}

	return v, nil
}
