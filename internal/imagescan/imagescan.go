// Package imagescan finds container images a CI build produced by scanning
// its console output (and small artifacts) for the lines common build tools
// print when they build or push an image.
package imagescan

import (
	"bufio"
	"errors"
	"regexp"
	"strings"

	"github.com/vinaycharlie01/jenkins-mcp-go/internal/imageref"
)

// Found is one image found in the output.
type Found struct {
	Ref      imageref.Ref
	Pushed   bool   // pushed to a registry (vs only built locally)
	Source   string // which pattern found it
	Evidence string // the matching line (trimmed)
}

const maxEvidence = 300

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

	// docker push: "The push refers to repository [r/app]" ... "1.4.2: digest: sha256:... size: 1570"
	dockerRepoRe   = regexp.MustCompile(`The push refers to repository \[([^\]\s]+)\]`)
	dockerDigestRe = regexp.MustCompile(`(?:^|\s)([A-Za-z0-9_][A-Za-z0-9_.\-]{0,127}): digest: (sha256:[0-9a-f]{64}) size: \d+`)
	// docker buildx / buildkit: "pushing manifest for r/app:1.4.2@sha256:..."
	buildxRe = regexp.MustCompile(`pushing manifest for (\S+@sha256:[0-9a-f]{64})`)
	// kaniko: "Pushing image to r/app:1.4.2" then "Pushed r/app@sha256:..."
	kanikoPushingRe = regexp.MustCompile(`Pushing image to (\S+)`)
	kanikoPushedRe  = regexp.MustCompile(`Pushed (\S+@sha256:[0-9a-f]{64})`)
	// jib: "Built and pushed image as r/app:1.4.2"
	jibRe = regexp.MustCompile(`Built and pushed image as (\S+)`)
	// built locally only: "Successfully tagged r/app:1.4.2" / buildkit "naming to r/app:1.4.2 done"
	builtRe = regexp.MustCompile(`(?:Successfully tagged|naming to) (\S+)`)
	// pinned reference anywhere (used for artifacts only, where it is not noisy)
	pinnedRe  = regexp.MustCompile(`([A-Za-z0-9][A-Za-z0-9._\-/:]*[A-Za-z0-9])@(sha256:[0-9a-f]{64})`)
	bareRe    = regexp.MustCompile(`^\s*(sha256:[0-9a-f]{64})\s*$`)
	trailDots = regexp.MustCompile(`[.,;]+$`)
)

// Scanner scans text for images. Custom is an optional operator-supplied
// pattern with named groups "image" and/or "digest" (and optional "tag").
type Scanner struct {
	Custom *regexp.Regexp
}

// CompileCustom validates an operator pattern (JENKINS_IMAGE_PATTERN).
func CompileCustom(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	if re.SubexpIndex("image") < 0 && re.SubexpIndex("digest") < 0 {
		return nil, errors.New("pattern needs a named group (?P<image>...) and/or (?P<digest>...)")
	}

	return re, nil
}

// Scan returns the images found in console text, de-duplicated.
func (s Scanner) Scan(text string) []Found {
	var out []Found
	dockerRepo := ""
	kanikoTags := map[string][]imageref.Ref{} // repository -> refs from "Pushing image to"

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := ansiRe.ReplaceAllString(sc.Text(), "")
		add := func(ref string, digest string, pushed bool, source string) {
			r, ok := imageref.Parse(trailDots.ReplaceAllString(ref, ""))
			if !ok {
				return
			}
			if digest != "" {
				r.Digest = digest
			}
			out = append(out, Found{Ref: r, Pushed: pushed, Source: source, Evidence: evidence(line)})
		}

		if m := dockerRepoRe.FindStringSubmatch(line); m != nil {
			dockerRepo = m[1]
		}
		if m := dockerDigestRe.FindStringSubmatch(line); m != nil && dockerRepo != "" {
			add(dockerRepo+":"+m[1], m[2], true, "docker push")
		}
		if m := buildxRe.FindStringSubmatch(line); m != nil {
			add(m[1], "", true, "buildx push")
		}
		if m := kanikoPushingRe.FindStringSubmatch(line); m != nil {
			if r, ok := imageref.Parse(m[1]); ok {
				kanikoTags[r.Repository] = append(kanikoTags[r.Repository], r)
			}
		}
		if m := kanikoPushedRe.FindStringSubmatch(line); m != nil {
			if r, ok := imageref.Parse(m[1]); ok {
				tags := kanikoTags[r.Repository]
				if len(tags) == 0 {
					add(m[1], "", true, "kaniko")
				}
				for _, t := range tags {
					add(t.Repository+":"+t.Tag, r.Digest, true, "kaniko")
				}
			}
		}
		if m := jibRe.FindStringSubmatch(line); m != nil {
			add(m[1], "", true, "jib")
		}
		if m := builtRe.FindStringSubmatch(line); m != nil {
			add(strings.TrimSuffix(m[1], " done"), "", false, "docker build")
		}
		if s.Custom != nil {
			s.custom(line, add)
		}
	}

	return dedupe(out)
}

func (s Scanner) custom(line string, add func(ref, digest string, pushed bool, source string)) {
	m := s.Custom.FindStringSubmatch(line)
	if m == nil {
		return
	}
	group := func(name string) string {
		if i := s.Custom.SubexpIndex(name); i >= 0 {
			return m[i]
		}

		return ""
	}
	image, tag, digest := group("image"), group("tag"), strings.ToLower(group("digest"))
	if image == "" {
		return
	}
	if tag != "" {
		image += ":" + tag
	}
	add(image, digest, true, "custom pattern")
}

// ScanArtifact scans a small artifact file (e.g. image-digest.txt,
// jib-image.digest). It also accepts pinned references anywhere and returns
// a bare digest file's digest separately.
func (s Scanner) ScanArtifact(name, content string) (found []Found, bareDigest string) {
	found = s.Scan(content)
	for _, m := range pinnedRe.FindAllStringSubmatch(content, -1) {
		if r, ok := imageref.Parse(m[1] + "@" + m[2]); ok {
			found = append(found, Found{Ref: r, Pushed: true, Source: "artifact " + name, Evidence: evidence(m[0])})
		}
	}
	if m := bareRe.FindStringSubmatch(content); m != nil {
		bareDigest = m[1]
	}

	return dedupe(found), bareDigest
}

// dedupe merges entries for the same repository+tag, preferring ones with a
// digest and ones that were pushed.
func dedupe(in []Found) []Found {
	var out []Found
	idx := map[string]int{}
	for _, f := range in {
		k := f.Ref.Repository + ":" + f.Ref.Tag
		if f.Ref.Tag == "" {
			k += "@" + f.Ref.Digest
		}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, f)

			continue
		}
		cur := &out[i]
		if cur.Ref.Digest == "" && f.Ref.Digest != "" {
			cur.Ref.Digest, cur.Source, cur.Evidence = f.Ref.Digest, f.Source, f.Evidence
		}
		if f.Pushed && !cur.Pushed {
			cur.Pushed = true
			if f.Ref.Digest != "" || cur.Ref.Digest == "" {
				cur.Source, cur.Evidence = f.Source, f.Evidence
			}
		}
	}

	return out
}

func evidence(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > maxEvidence {
		line = line[:maxEvidence] + "…"
	}

	return line
}
