// Package imageref parses and normalizes container image references so that
// images reported by different systems (Kubernetes, CI logs) can be compared.
package imageref

import (
	"regexp"
	"strings"
)

const defaultRegistry = "docker.io"

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Ref is a parsed image reference.
type Ref struct {
	// Repository is the normalized repository including the registry,
	// e.g. docker.io/library/nginx or registry.example.com/team/app.
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"` // sha256:<64 hex>
}

// String renders repository[:tag][@digest].
func (r Ref) String() string {
	s := r.Repository
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}

	return s
}

// Parse parses an image reference such as "nginx", "nginx:1.27",
// "registry.example.com:5000/team/app:1.4.2@sha256:...". It returns false for
// strings that are not image references.
func Parse(s string) (Ref, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n\"'") {
		return Ref{}, false
	}
	var r Ref
	if i := strings.Index(s, "@"); i >= 0 {
		r.Digest = strings.ToLower(s[i+1:])
		if !digestRe.MatchString(r.Digest) {
			return Ref{}, false
		}
		s = s[:i]
	}
	// A tag is after the last ':' that comes after the last '/'.
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		r.Tag = s[i+1:]
		s = s[:i]
		if r.Tag == "" {
			return Ref{}, false
		}
	}
	if s == "" || strings.HasPrefix(s, "sha256") {
		return Ref{}, false
	}
	r.Repository = Normalize(s)

	return r, true
}

// Normalize returns the canonical repository name: a registry host is added
// for Docker Hub names, "library/" for official images, and the registry host
// is lower-cased.
func Normalize(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 1 {
		return defaultRegistry + "/library/" + parts[0]
	}
	host := parts[0]
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return defaultRegistry + "/" + repo
	}
	host = strings.ToLower(host)
	if host == "index.docker.io" || host == "registry-1.docker.io" {
		host = defaultRegistry
	}

	return host + "/" + parts[1]
}

// DigestFromImageID extracts the registry (repo) digest from a Kubernetes
// containerStatus.imageID. Forms:
//
//	docker-pullable://registry/repo@sha256:...  -> registry digest
//	registry/repo@sha256:...                    -> registry digest (containerd)
//	docker://sha256:... or sha256:...           -> local image ID, not comparable
func DigestFromImageID(id string) string {
	for _, p := range []string{"docker-pullable://", "docker://", "containerd://", "cri-o://"} {
		id = strings.TrimPrefix(id, p)
	}
	i := strings.LastIndex(id, "@")
	if i < 0 {
		return ""
	}
	d := strings.ToLower(id[i+1:])
	if !digestRe.MatchString(d) {
		return ""
	}

	return d
}
