package jenkins

import (
	"encoding/json"
	"strings"
	"testing"

	client "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/imagescan"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/toolkit"
)

var (
	dA = "sha256:" + strings.Repeat("aa", 32)
	dB = "sha256:" + strings.Repeat("bb", 32)
)

func buildWith(t *testing.T, raw string) *client.Build {
	t.Helper()
	var b client.Build
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}

	return &b
}

func TestBuildImagesFromDockerPush(t *testing.T) {
	api := &fakeAPI{
		build: buildWith(t, `{"number":42,"result":"SUCCESS","displayName":"#42 1.4.2",
			"actions":[{"parameters":[{"name":"IMAGE_TAG","value":"1.4.2"},{"name":"DEPLOY_ENV","value":"qa"}]},
			{"lastBuiltRevision":{"SHA1":"abc123","branch":[{"name":"origin/main"}]}}]}`),
		console: &client.Console{Size: 9000, Start: 100, Text: "Successfully tagged registry.example.com/team/payments:1.4.2\n" +
			"The push refers to repository [registry.example.com/team/payments]\n1.4.2: digest: " + dA + " size: 1570\n"},
	}
	var out buildImagesOutput
	call(t, session(t, api), "jenkins_get_build_images", map[string]any{"job": "payments/main"}, &out)

	if out.Build != 42 || len(out.Images) != 1 || len(out.Expected) != 1 {
		t.Fatalf("%+v", out)
	}
	im := out.Images[0]
	if im.Image != "registry.example.com/team/payments:1.4.2" || im.Digest != dA || !im.Pushed || im.Source != "docker push" {
		t.Fatalf("%+v", im)
	}
	if e := out.Expected[0]; e.Image != im.Image || e.Digest != dA {
		t.Fatalf("expected %+v", e)
	}
	if out.Hints.Parameters["IMAGE_TAG"] != "1.4.2" || out.Hints.Parameters["DEPLOY_ENV"] != "" || out.Hints.GitCommit != "abc123" {
		t.Fatalf("hints %+v", out.Hints)
	}
	// "lastBuild" is resolved once; the console must come from build 42.
	if api.start != -1 || api.maxBytes != defaultScanBytes || api.ref != "42" {
		t.Fatalf("console call: start=%d max=%d ref=%s", api.start, api.maxBytes, api.ref)
	}
	if len(out.Notes) == 0 || !strings.Contains(out.Notes[0], "scanned the last") {
		t.Fatalf("partial-scan note missing: %v", out.Notes)
	}
}

func TestBuildImagesJibWithDigestArtifact(t *testing.T) {
	api := &fakeAPI{
		build: buildWith(t, `{"number":7,"result":"SUCCESS","artifacts":[
			{"fileName":"app.jar","relativePath":"target/app.jar"},
			{"fileName":"jib-image.digest","relativePath":"target/jib-image.digest"}]}`),
		console: &client.Console{Text: "[INFO] Built and pushed image as registry.example.com/team/jibapp:9.9\n"},
		files:   map[string]string{"target/jib-image.digest": dB + "\n"},
	}
	var out buildImagesOutput
	call(t, session(t, api), "jenkins_get_build_images", map[string]any{"job": "jibapp", "build": "7"}, &out)
	if len(out.ArtifactsScanned) != 1 || out.ArtifactsScanned[0] != "target/jib-image.digest" {
		t.Fatalf("artifact selection: %v", out.ArtifactsScanned)
	}
	if im := out.Images[0]; im.Digest != dB || !strings.Contains(im.Source, "digest artifact") {
		t.Fatalf("%+v", im)
	}
	for _, r := range api.refs {
		if r != "7" {
			t.Fatalf("all calls must target build 7: %v", api.refs)
		}
	}
}

func TestBuildImagesNothingFoundAndBuiltOnly(t *testing.T) {
	api := &fakeAPI{build: buildWith(t, `{"number":3,"building":true}`), console: &client.Console{Text: "npm test\nok\n"}}
	var out buildImagesOutput
	call(t, session(t, api), "jenkins_get_build_images", map[string]any{"job": "svc"}, &out)
	if len(out.Images) != 0 || len(out.Expected) != 0 || out.Result != "BUILDING" || !strings.Contains(strings.Join(out.Notes, " "), "JENKINS_IMAGE_PATTERN") {
		t.Fatalf("%+v", out)
	}

	api = &fakeAPI{build: buildWith(t, `{"number":4}`), console: &client.Console{Text: "naming to registry.example.com/team/svc:4 done\n"}}
	call(t, session(t, api), "jenkins_get_build_images", map[string]any{"job": "svc"}, &out)
	if len(out.Expected) != 1 || out.Images[0].Pushed || !strings.Contains(strings.Join(out.Notes, " "), "no push was found") {
		t.Fatalf("%+v", out)
	}
}

func TestBuildImagesCustomPattern(t *testing.T) {
	re, err := imagescan.CompileCustom(`PUBLISHED (?P<image>\S+) (?P<digest>sha256:[0-9a-f]{64})`)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{build: buildWith(t, `{"number":5}`), console: &client.Console{Text: "PUBLISHED registry.example.com/team/svc:5 " + dA + "\n"}}
	var out buildImagesOutput
	call(t, session(t, api, Options{ImagePattern: re}), "jenkins_get_build_images", map[string]any{"job": "svc"}, &out)
	if len(out.Expected) != 1 || out.Expected[0].Digest != dA || out.Images[0].Source != "custom pattern" {
		t.Fatalf("%+v", out)
	}
}

func TestBuildImagesValidation(t *testing.T) {
	api := &fakeAPI{}
	cs := session(t, api)
	for _, args := range []map[string]any{
		{"job": ""}, {"job": "../x"}, {"job": "svc", "build": "nope"}, {"job": "svc", "scanBytes": 64 << 20},
	} {
		if c := code(t, call(t, cs, "jenkins_get_build_images", args, nil)); c != toolkit.CodeInvalidInput {
			t.Errorf("%v: %s", args, c)
		}
	}
	if api.calls != 0 {
		t.Fatal("no API calls expected")
	}
}
