package jenkins

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vinaycharlie01/jenkins-mcp-go/internal/imagescan"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

const (
	defaultScanBytes = 4 << 20
	maxScanBytes     = 32 << 20
	maxArtifacts     = 5
	maxArtifactBytes = 64 << 10
)

var (
	// artifactRe selects small files likely to record image references or digests.
	artifactRe = regexp.MustCompile(`(?i)(digest|image)[^/]*\.(txt|json|digest|id|properties|env)$|(?i)\.digest$`)
	// paramRe selects build parameters that usually carry image info.
	paramRe = regexp.MustCompile(`(?i)image|tag|version|digest|sha`)
)

type buildImagesInput struct {
	Job       string `json:"job" jsonschema:"Job path. See tool description of other jenkins tools."`
	Build     string `json:"build,omitempty" jsonschema:"Build number, or lastBuild (default), lastSuccessfulBuild, ..."`
	ScanBytes int    `json:"scanBytes,omitempty" jsonschema:"How many bytes from the END of the console log to scan (default 4 MiB, max 32 MiB)."`
}

// BuildImage is one image the build produced.
type BuildImage struct {
	Image      string `json:"image"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Pushed     bool   `json:"pushed"`
	Source     string `json:"source"`
	Evidence   string `json:"evidence"`
}

// ExpectedImage matches argocd_get_images' "expected" input.
type ExpectedImage struct {
	Image  string `json:"image"`
	Digest string `json:"digest,omitempty"`
}

type buildHints struct {
	DisplayName     string            `json:"displayName,omitempty"`
	Description     string            `json:"description,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	GitCommit       string            `json:"gitCommit,omitempty"`
	GitBranches     []string          `json:"gitBranches,omitempty"`
	ArtifactDigests map[string]string `json:"artifactDigests,omitempty"`
}

type buildImagesOutput struct {
	Job              string          `json:"job"`
	Build            int             `json:"build"`
	Result           string          `json:"result,omitempty"`
	Images           []BuildImage    `json:"images"`
	Expected         []ExpectedImage `json:"expected"`
	Hints            buildHints      `json:"hints"`
	ConsoleScanned   int64           `json:"consoleBytesScanned"`
	ConsoleLogSize   int64           `json:"consoleLogSize"`
	ArtifactsScanned []string        `json:"artifactsScanned,omitempty"`
	Notes            []string        `json:"notes,omitempty"`
}

func (t *Tools) getBuildImages(ctx context.Context, _ *mcp.CallToolRequest, in buildImagesInput) (*mcp.CallToolResult, buildImagesOutput, error) {
	path, err := jobPath("job", in.Job, true)
	if err != nil {
		return nil, buildImagesOutput{}, err
	}
	ref, err := buildRef(in.Build)
	if err != nil {
		return nil, buildImagesOutput{}, err
	}
	scanBytes, err := toolkit.Limit("scanBytes", in.ScanBytes, defaultScanBytes, maxScanBytes)
	if err != nil {
		return nil, buildImagesOutput{}, err
	}

	b, err := t.api.GetBuild(ctx, path, ref)
	if err != nil {
		return nil, buildImagesOutput{}, wrap(err)
	}
	// Pin the build number so the console and artifacts come from the same build
	// even if "lastBuild" moves meanwhile.
	num := fmt.Sprint(b.Number)
	out := buildImagesOutput{Job: strings.Join(path, "/"), Build: b.Number, Result: b.Result, Images: []BuildImage{}, Expected: []ExpectedImage{}}
	if b.Building {
		out.Result = "BUILDING"
		out.Notes = append(out.Notes, "the build is still running; images pushed later are not included yet")
	}

	// Hints from build metadata.
	out.Hints.DisplayName, out.Hints.Description = b.DisplayName, b.Description
	for _, a := range b.Actions {
		for _, p := range a.Parameters {
			if paramRe.MatchString(p.Name) {
				if out.Hints.Parameters == nil {
					out.Hints.Parameters = map[string]string{}
				}
				out.Hints.Parameters[p.Name] = fmt.Sprint(p.Value)
			}
		}
		if r := a.LastBuiltRevision; r != nil && out.Hints.GitCommit == "" {
			out.Hints.GitCommit = r.SHA1
			for _, br := range r.Branch {
				out.Hints.GitBranches = append(out.Hints.GitBranches, br.Name)
			}
		}
	}

	// Console log (tail): where push output is.
	var found []imagescan.Found
	con, err := t.api.ConsoleText(ctx, path, num, -1, scanBytes)
	if err != nil {
		return nil, buildImagesOutput{}, wrap(err)
	}
	out.ConsoleScanned, out.ConsoleLogSize = int64(len(con.Text)), con.Size
	if con.Start > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("scanned the last %d of %d console bytes; raise scanBytes if pushes happen earlier", len(con.Text), con.Size))
	}
	found = append(found, t.scanner.Scan(con.Text)...)

	// Small image/digest artifacts.
	var bareDigests []string
	for _, a := range b.Artifacts {
		if len(out.ArtifactsScanned) >= maxArtifacts {
			break
		}
		if !artifactRe.MatchString(a.RelativePath) {
			continue
		}
		data, _, err := t.api.GetArtifact(ctx, path, num, a.RelativePath, maxArtifactBytes)
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("artifact %s: %v", a.RelativePath, err))

			continue
		}
		out.ArtifactsScanned = append(out.ArtifactsScanned, a.RelativePath)
		fs, bare := t.scanner.ScanArtifact(a.RelativePath, string(data))
		found = append(found, fs...)
		if bare != "" {
			if out.Hints.ArtifactDigests == nil {
				out.Hints.ArtifactDigests = map[string]string{}
			}
			out.Hints.ArtifactDigests[a.RelativePath] = bare
			bareDigests = append(bareDigests, bare)
		}
	}

	images := mergeFound(found)
	// A single bare digest file (e.g. jib-image.digest) belongs to the single
	// pushed image that has no digest yet.
	if len(bareDigests) == 1 {
		var missing []int
		for i, im := range images {
			if im.Pushed && im.Ref.Digest == "" {
				missing = append(missing, i)
			}
		}
		if len(missing) == 1 {
			images[missing[0]].Ref.Digest = bareDigests[0]
			images[missing[0]].Source += " + digest artifact"
		}
	}

	anyPushed := false
	for _, f := range images {
		anyPushed = anyPushed || f.Pushed
	}
	for _, f := range images {
		out.Images = append(out.Images, BuildImage{
			Image: imageString(f), Repository: f.Ref.Repository, Tag: f.Ref.Tag, Digest: f.Ref.Digest,
			Pushed: f.Pushed, Source: f.Source, Evidence: f.Evidence,
		})
		if f.Pushed || !anyPushed {
			out.Expected = append(out.Expected, ExpectedImage{Image: imageString(f), Digest: f.Ref.Digest})
		}
	}
	sort.SliceStable(out.Images, func(i, j int) bool { return out.Images[i].Pushed && !out.Images[j].Pushed })

	switch {
	case len(out.Images) == 0:
		out.Notes = append(out.Notes, "no image found in the console tail or artifacts; if your pipeline prints images differently, "+
			"set JENKINS_IMAGE_PATTERN (see README) or check the hints (parameters, display name)")
	case !anyPushed:
		out.Notes = append(out.Notes, "images were built but no push was found; 'expected' lists the built tags")
	}
	for _, e := range out.Expected {
		if e.Digest == "" {
			out.Notes = append(out.Notes, "some images have no digest; argocd_get_images can then only compare tags (MATCH_TAG_ONLY)")

			break
		}
	}

	return nil, out, nil
}

func mergeFound(in []imagescan.Found) []imagescan.Found {
	// imagescan de-duplicates within one source; merge across console + artifacts.
	byKey := map[string]int{}
	var out []imagescan.Found
	for _, f := range in {
		k := f.Ref.Repository + ":" + f.Ref.Tag
		if f.Ref.Tag == "" {
			k += "@" + f.Ref.Digest
		}
		if i, ok := byKey[k]; ok {
			if out[i].Ref.Digest == "" && f.Ref.Digest != "" {
				out[i].Ref.Digest = f.Ref.Digest
				out[i].Source += " + " + f.Source
			}
			out[i].Pushed = out[i].Pushed || f.Pushed

			continue
		}
		byKey[k] = len(out)
		out = append(out, f)
	}

	return out
}

// imageString renders repository:tag (the digest is reported separately).
func imageString(f imagescan.Found) string {
	if f.Ref.Tag != "" {
		return f.Ref.Repository + ":" + f.Ref.Tag
	}

	return f.Ref.Repository + "@" + f.Ref.Digest
}
