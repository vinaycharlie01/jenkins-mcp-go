package imagescan

import (
	"strings"
	"testing"
)

var (
	d1 = "sha256:" + strings.Repeat("1a", 32)
	d2 = "sha256:" + strings.Repeat("2b", 32)
)

func find(t *testing.T, fs []Found, ref string) Found {
	t.Helper()
	for _, f := range fs {
		if f.Ref.Repository+":"+f.Ref.Tag == ref {
			return f
		}
	}
	t.Fatalf("%s not found in %+v", ref, fs)

	return Found{}
}

func TestDockerPush(t *testing.T) {
	log := `[2026-01-01T10:00:00Z] + docker build -t registry.example.com/team/payments:1.4.2 .
Successfully tagged registry.example.com/team/payments:1.4.2
+ docker push registry.example.com/team/payments:1.4.2
The push refers to repository [registry.example.com/team/payments]
5f70bf18a086: Pushed
1.4.2: digest: ` + d1 + ` size: 1570
+ docker push registry.example.com/team/payments:latest
The push refers to repository [registry.example.com/team/payments]
latest: digest: ` + d1 + ` size: 1570`
	fs := Scanner{}.Scan(log)
	f := find(t, fs, "registry.example.com/team/payments:1.4.2")
	if f.Ref.Digest != d1 || !f.Pushed || f.Source != "docker push" || !strings.Contains(f.Evidence, "1.4.2: digest:") {
		t.Fatalf("%+v", f)
	}
	if l := find(t, fs, "registry.example.com/team/payments:latest"); l.Ref.Digest != d1 {
		t.Fatalf("%+v", l)
	}
	if len(fs) != 2 {
		t.Fatalf("built + pushed should merge: %+v", fs)
	}
}

func TestBuildxKanikoJib(t *testing.T) {
	log := "#12 \x1b[34mpushing manifest for ghcr.io/o/web:2.0@" + d1 + "\x1b[0m 0.4s done\n" +
		"INFO[0010] Pushing image to registry.example.com/team/api:3.1\n" +
		"INFO[0011] Pushing image to registry.example.com/team/api:latest\n" +
		"INFO[0020] Pushed registry.example.com/team/api@" + d2 + "\n" +
		"[INFO] Built and pushed image as registry.example.com/team/jibapp:9.9\n"
	fs := Scanner{}.Scan(log)
	if f := find(t, fs, "ghcr.io/o/web:2.0"); f.Ref.Digest != d1 || f.Source != "buildx push" {
		t.Fatalf("%+v", f)
	}
	for _, tag := range []string{"3.1", "latest"} {
		if f := find(t, fs, "registry.example.com/team/api:"+tag); f.Ref.Digest != d2 || f.Source != "kaniko" {
			t.Fatalf("%+v", f)
		}
	}
	if f := find(t, fs, "registry.example.com/team/jibapp:9.9"); !f.Pushed || f.Ref.Digest != "" {
		t.Fatalf("%+v", f)
	}
}

func TestBuiltOnlyAndNoise(t *testing.T) {
	log := "#9 naming to docker.io/library/tool:dev done\n" +
		"Step 1/5 : FROM node:20@" + d1 + "\n" + // base image: must NOT be reported
		"Digest: " + d2 + "\nStatus: Downloaded newer image for node:20\n" // docker pull: must NOT be reported
	fs := Scanner{}.Scan(log)
	if len(fs) != 1 {
		t.Fatalf("only the built image expected: %+v", fs)
	}
	if f := fs[0]; f.Pushed || f.Ref.Repository != "docker.io/library/tool" || f.Ref.Tag != "dev" {
		t.Fatalf("%+v", f)
	}
}

func TestCustomPattern(t *testing.T) {
	re, err := CompileCustom(`IMAGE_PUBLISHED=(?P<image>\S+) TAG=(?P<tag>\S+) DIGEST=(?P<digest>sha256:[0-9a-f]{64})`)
	if err != nil {
		t.Fatal(err)
	}
	fs := Scanner{Custom: re}.Scan("IMAGE_PUBLISHED=registry.example.com/team/svc TAG=7 DIGEST=" + d1)
	if f := find(t, fs, "registry.example.com/team/svc:7"); f.Ref.Digest != d1 || f.Source != "custom pattern" {
		t.Fatalf("%+v", f)
	}
	if _, err := CompileCustom(`no groups`); err == nil {
		t.Fatal("pattern without named groups must be rejected")
	}
	if _, err := CompileCustom(`(?P<image>[`); err == nil {
		t.Fatal("invalid regex must be rejected")
	}
	if re, err := CompileCustom(""); re != nil || err != nil {
		t.Fatal("empty pattern = disabled")
	}
}

func TestScanArtifact(t *testing.T) {
	fs, bare := Scanner{}.ScanArtifact("image.json", `{"image":"registry.example.com/team/svc:7@`+d1+`"}`)
	if len(fs) != 1 || fs[0].Ref.Tag != "7" || fs[0].Ref.Digest != d1 || bare != "" {
		t.Fatalf("%+v %q", fs, bare)
	}
	if _, bare := (Scanner{}).ScanArtifact("jib-image.digest", d2+"\n"); bare != d2 {
		t.Fatalf("bare digest %q", bare)
	}
}
