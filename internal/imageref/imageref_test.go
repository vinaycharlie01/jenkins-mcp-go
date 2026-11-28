package imageref

import (
	"strings"
	"testing"
)

var dg = "sha256:" + strings.Repeat("ab", 32)

func TestParse(t *testing.T) {
	cases := map[string]Ref{
		"nginx":                                  {Repository: "docker.io/library/nginx"},
		"nginx:1.27":                             {Repository: "docker.io/library/nginx", Tag: "1.27"},
		"bitnami/redis:7":                        {Repository: "docker.io/bitnami/redis", Tag: "7"},
		"Registry.Example.com:5000/team/app:1.4": {Repository: "registry.example.com:5000/team/app", Tag: "1.4"},
		"localhost/app":                          {Repository: "localhost/app"},
		"index.docker.io/library/nginx:1":        {Repository: "docker.io/library/nginx", Tag: "1"},
		"ghcr.io/o/app:v1@" + dg:                 {Repository: "ghcr.io/o/app", Tag: "v1", Digest: dg},
		"ghcr.io/o/app@" + dg:                    {Repository: "ghcr.io/o/app", Digest: dg},
	}
	for in, want := range cases {
		got, ok := Parse(in)
		if !ok || got != want {
			t.Errorf("Parse(%q) = %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "a b", "app@sha256:short", "app:", "sha256:" + strings.Repeat("a", 64)} {
		if _, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	if s := (Ref{Repository: "ghcr.io/o/app", Tag: "v1", Digest: dg}).String(); s != "ghcr.io/o/app:v1@"+dg {
		t.Error(s)
	}
}

func TestDigestFromImageID(t *testing.T) {
	for in, want := range map[string]string{
		"docker-pullable://registry.example.com/app@" + dg: dg,
		"registry.example.com/app@" + dg:                   dg,
		"docker://" + dg:                                   "",
		dg:                                                 "",
		"":                                                 "",
	} {
		if got := DigestFromImageID(in); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}
