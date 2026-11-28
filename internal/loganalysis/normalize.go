package loganalysis

import (
	"regexp"
	"strings"
)

// Normalization turns dynamic values into placeholders so repeated messages
// group together ("user 42 logged in from 10.0.0.1" -> "user {n} logged in from {ip}").
var normalizers = []struct {
	guard func(string) bool // cheap check before running the regex
	re    *regexp.Regexp
	with  string
}{
	{hasAll("-"), regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "{uuid}"},
	{hasAll("-", ":"), regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`), "{ts}"},
	{hasAll(":"), regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`), "{time}"},
	{hasAll("@"), regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "{email}"},
	{hasAll("://"), regexp.MustCompile(`\b(?:https?|amqps?|sqs|wss?)://[^\s"'<>]+`), "{url}"},
	{hasAll("."), regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`), "{ip}"},
	{nil, regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]{8,}\b`), "{hex}"},
	{nil, regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|s|m|h|µs|ns|kb|mb|gb|b)?\b`), "{n}"},
}

var spaceRe = regexp.MustCompile(`\s+`)

func hasAll(subs ...string) func(string) bool {
	return func(s string) bool {
		for _, x := range subs {
			if !strings.Contains(s, x) {
				return false
			}
		}

		return true
	}
}

const maxPatternLen = 300

// Normalize returns the message pattern of s.
func Normalize(s string) string {
	digit := strings.ContainsAny(s, "0123456789")
	for _, n := range normalizers {
		// Every placeholder except {email} and {url} needs a digit.
		if !digit && n.with != "{email}" && n.with != "{url}" && n.with != "{hex}" {
			continue
		}
		if n.guard != nil && !n.guard(s) {
			continue
		}
		s = n.re.ReplaceAllString(s, n.with)
	}
	if strings.ContainsAny(s, "\t\n\r") || strings.Contains(s, "  ") {
		s = spaceRe.ReplaceAllString(s, " ")
	}
	s = strings.TrimSpace(s)
	if len(s) > maxPatternLen {
		s = s[:maxPatternLen] + "…"
	}

	return s
}

var pathIDRe = regexp.MustCompile(`^(?:\d+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{16,}|[A-Za-z0-9_-]{20,})$`)

// NormalizePath replaces ID-like path segments with {id} for endpoint grouping.
func NormalizePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s != "" && pathIDRe.MatchString(s) {
			segs[i] = "{id}"
		}
	}

	return strings.Join(segs, "/")
}
