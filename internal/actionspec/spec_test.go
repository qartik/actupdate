package actionspec

import "testing"

func TestParseRemoteAction(t *testing.T) {
	spec, err := Parse("actions/checkout@v4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.Kind != KindRemote || spec.Repo != "actions/checkout" || spec.Ref != "v4" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

func TestParseReusableWorkflow(t *testing.T) {
	spec, err := Parse("owner/repo/.github/workflows/release.yml@v2")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.Kind != KindRemote || spec.Repo != "owner/repo" || spec.Ref != "v2" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

func TestParseKinds(t *testing.T) {
	cases := map[string]Kind{
		"./local-action":   KindLocal,
		"docker://alpine":  KindDocker,
		"malformed-action": KindUnsupported,
	}
	for input, expected := range cases {
		spec, _ := Parse(input)
		if spec.Kind != expected {
			t.Fatalf("%q expected kind %v got %v", input, expected, spec.Kind)
		}
	}
}

func TestParseMajor(t *testing.T) {
	major, err := ParseMajor("v6.2.1")
	if err != nil {
		t.Fatalf("parse major: %v", err)
	}
	if major != 6 {
		t.Fatalf("expected major 6, got %d", major)
	}
}

func TestParseMajorRejectsPreRelease(t *testing.T) {
	if _, err := ParseMajor("v6.0.0-rc1"); err == nil {
		t.Fatal("expected pre-release to be rejected")
	}
}

func TestIsCommitSHA(t *testing.T) {
	if !IsCommitSHA("0123456789abcdef0123456789abcdef01234567") {
		t.Fatal("expected commit SHA match")
	}
	if IsCommitSHA("main") {
		t.Fatal("did not expect branch ref to match SHA")
	}
}
