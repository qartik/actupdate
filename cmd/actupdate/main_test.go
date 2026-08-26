package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func withVersion(t *testing.T, value string) {
	t.Helper()
	previous := version
	version = value
	t.Cleanup(func() {
		version = previous
	})
}

func TestParseArgsCooldownDays(t *testing.T) {
	opts, err := parseArgs([]string{"--cooldown-days", "7"})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if opts.CooldownDays != 7 {
		t.Fatalf("expected cooldown 7, got %d", opts.CooldownDays)
	}
}

func TestParseArgsIncludeCompositeActions(t *testing.T) {
	opts, err := parseArgs([]string{"--include-composite-actions"})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if !opts.IncludeCompositeActions {
		t.Fatal("expected include composite actions to be enabled")
	}
}

func TestParseArgsRejectsNegativeCooldownDays(t *testing.T) {
	if _, err := parseArgs([]string{"--cooldown-days", "-1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseArgsRejectsOverflowingCooldownDays(t *testing.T) {
	tooLarge := fmt.Sprintf("%d", maxCooldownDays+1)
	if _, err := parseArgs([]string{"--cooldown-days", tooLarge}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunVersion(t *testing.T) {
	withVersion(t, "1.2.3")

	var stdout bytes.Buffer
	exitCode := run([]string{"version"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, http.DefaultClient, "", "")
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if got := strings.TrimSpace(stdout.String()); got != "1.2.3" {
		t.Fatalf("unexpected version output: %q", got)
	}
}

func TestVersionFromBuildInfoUsesModuleVersion(t *testing.T) {
	got := versionFromBuildInfo(&debug.BuildInfo{
		Main: debug.Module{Version: "v0.2.0"},
	})
	if got != "v0.2.0" {
		t.Fatalf("unexpected version: %q", got)
	}
}

func TestVersionFromBuildInfoUsesVCSRevisionForDevelopmentBuild(t *testing.T) {
	got := versionFromBuildInfo(&debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.modified", Value: "true"},
		},
	})
	if got != "devel-0123456789ab+dirty" {
		t.Fatalf("unexpected version: %q", got)
	}
}

func TestRunHelpWithOtherFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--help", "--repo", "."}, strings.NewReader(""), &stdout, &stderr, http.DefaultClient, "", "")
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "Usage of actupdate:") {
		t.Fatalf("expected usage output, got %q", got)
	}
}

func TestRunInvalidFlagUsesStderrOnly(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--unknown"}, strings.NewReader(""), &stdout, &stderr, http.DefaultClient, "", "")
	if exitCode != exitInvalidInput {
		t.Fatalf("expected invalid input exit, got %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "flag provided but not defined") {
		t.Fatalf("expected flag error on stderr, got %q", got)
	}
}

func TestRunNoFilesFound(t *testing.T) {
	repo := t.TempDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo}, strings.NewReader(""), &stdout, &stderr, http.DefaultClient, "", "")
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "No workflow files found") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestRunNoFilesFoundWithCompositeActionsEnabled(t *testing.T) {
	repo := t.TempDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--include-composite-actions"}, strings.NewReader(""), &stdout, &stderr, http.DefaultClient, "", "")
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "No workflow or composite action files found") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestPromptConfirmDefaultsToYes(t *testing.T) {
	var stdout bytes.Buffer
	confirmed, err := promptConfirm(strings.NewReader("\n"), &stdout)
	if err != nil {
		t.Fatalf("promptConfirm: %v", err)
	}
	if !confirmed {
		t.Fatal("expected empty input to confirm")
	}
	if got := stdout.String(); got != "Apply these updates? [Y/n]: " {
		t.Fatalf("unexpected prompt %q", got)
	}
}

func TestPromptConfirmRejectsNo(t *testing.T) {
	var stdout bytes.Buffer
	confirmed, err := promptConfirm(strings.NewReader("n\n"), &stdout)
	if err != nil {
		t.Fatalf("promptConfirm: %v", err)
	}
	if confirmed {
		t.Fatal("expected no to reject")
	}
}

func TestRunApplyYes(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "release.yml")
	original := "steps:\n  - uses: actions/checkout@v4\n"
	if err := os.WriteFile(workflowPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/actions/checkout/tags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"name":"v6"},{"name":"v5"},{"name":"v4"}]`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--yes"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL, t.TempDir())
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}

	updated, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	if got := string(updated); !strings.Contains(got, "actions/checkout@v6") {
		t.Fatalf("expected updated workflow, got %q", got)
	}
	if !strings.Contains(stdout.String(), "actions/checkout@v4 -> @v6") {
		t.Fatalf("expected plan output, got %q", stdout.String())
	}
}

func TestRunApplyYesWithCooldownDays(t *testing.T) {
	oldEnough := time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "release.yml")
	original := "steps:\n  - uses: actions/checkout@v4\n"
	if err := os.WriteFile(workflowPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/checkout/tags":
			fmt.Fprint(w, `[{"name":"v6"},{"name":"v4"}]`)
		case "/repos/actions/checkout/git/ref/tags/v6":
			fmt.Fprint(w, `{"object":{"type":"tag","sha":"tag-v6"}}`)
		case "/repos/actions/checkout/git/tags/tag-v6":
			fmt.Fprintf(w, `{"tagger":{"date":"%s"}}`, oldEnough)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--yes", "--cooldown-days", "7"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL, t.TempDir())
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}

	updated, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	if got := string(updated); !strings.Contains(got, "actions/checkout@v6") {
		t.Fatalf("expected updated workflow, got %q", got)
	}
}

func TestRunCooldownDaysLeavesTooNewMajorUnchanged(t *testing.T) {
	tooNewMoving := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	tooNewExact := time.Now().Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339)
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "release.yml")
	original := "steps:\n  - uses: actions/checkout@v4\n"
	if err := os.WriteFile(workflowPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/checkout/tags":
			fmt.Fprint(w, `[{"name":"v6"},{"name":"v6.2.1"},{"name":"v4"}]`)
		case "/repos/actions/checkout/git/ref/tags/v6":
			fmt.Fprint(w, `{"object":{"type":"tag","sha":"tag-v6"}}`)
		case "/repos/actions/checkout/git/ref/tags/v6.2.1":
			fmt.Fprint(w, `{"object":{"type":"tag","sha":"tag-v621"}}`)
		case "/repos/actions/checkout/git/tags/tag-v6":
			fmt.Fprintf(w, `{"tagger":{"date":"%s"}}`, tooNewMoving)
		case "/repos/actions/checkout/git/tags/tag-v621":
			fmt.Fprintf(w, `{"tagger":{"date":"%s"}}`, tooNewExact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--yes", "--cooldown-days", "7"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL, t.TempDir())
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}

	updated, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	if string(updated) != original {
		t.Fatalf("workflow should remain unchanged, got %q", string(updated))
	}
	if !strings.Contains(stdout.String(), "unchanged: newer major tags are still within cooldown") {
		t.Fatalf("expected cooldown reason in output, got %q", stdout.String())
	}
}

func TestRunUpdatesOlderExactTagEvenWhenSameRepoHasNewerRef(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "build_wheels.yml")
	original := "steps:\n  - uses: pypa/cibuildwheel@v3.3\n  - uses: pypa/cibuildwheel@v3.0\n"
	if err := os.WriteFile(workflowPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/pypa/cibuildwheel/tags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"name":"v3.3"},{"name":"v3.0"},{"name":"v2.9"}]`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--yes"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL, t.TempDir())
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}

	updated, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	got := string(updated)
	if strings.Count(got, "pypa/cibuildwheel@v3.3") != 2 {
		t.Fatalf("expected both refs to end at v3.3, got %q", got)
	}
	if !strings.Contains(stdout.String(), "pypa/cibuildwheel@v3.0 -> @v3.3") {
		t.Fatalf("expected same-major upgrade in output, got %q", stdout.String())
	}
}

func TestRunVerificationFailurePreventsWrites(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "release.yml")
	original := "steps:\n  - uses: actions/checkout@v4\n"
	if err := os.WriteFile(workflowPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--yes"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL, t.TempDir())
	if exitCode != exitVerificationFailure {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}

	updated, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	if string(updated) != original {
		t.Fatalf("workflow should remain unchanged, got %q", string(updated))
	}
}

func TestRunIncludeCompositeActionsUpdatesNestedActionMetadata(t *testing.T) {
	repo := t.TempDir()
	compositeDir := filepath.Join(repo, "py-release-checks")
	if err := os.MkdirAll(compositeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	actionPath := filepath.Join(compositeDir, "action.yml")
	original := strings.Join([]string{
		"name: release checks",
		"runs:",
		"  using: composite",
		"  steps:",
		"    - uses: actions/checkout@v4",
		"",
	}, "\n")
	if err := os.WriteFile(actionPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write action metadata: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/actions/checkout/tags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"name":"v6"},{"name":"v5"},{"name":"v4"}]`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--repo", repo, "--yes", "--include-composite-actions"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL, t.TempDir())
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d stderr=%s", exitCode, stderr.String())
	}

	updated, err := os.ReadFile(actionPath)
	if err != nil {
		t.Fatalf("read action metadata: %v", err)
	}
	if got := string(updated); !strings.Contains(got, "actions/checkout@v6") {
		t.Fatalf("expected updated action metadata, got %q", got)
	}
	if !strings.Contains(stdout.String(), "py-release-checks/action.yml") {
		t.Fatalf("expected composite action path in output, got %q", stdout.String())
	}
}
