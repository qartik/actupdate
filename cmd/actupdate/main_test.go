package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseArgsCooldownDays(t *testing.T) {
	opts, err := parseArgs([]string{"--cooldown-days", "7"}, io.Discard)
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if opts.CooldownDays != 7 {
		t.Fatalf("expected cooldown 7, got %d", opts.CooldownDays)
	}
}

func TestParseArgsRejectsNegativeCooldownDays(t *testing.T) {
	if _, err := parseArgs([]string{"--cooldown-days", "-1"}, io.Discard); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseArgsRejectsOverflowingCooldownDays(t *testing.T) {
	tooLarge := fmt.Sprintf("%d", maxCooldownDays+1)
	if _, err := parseArgs([]string{"--cooldown-days", tooLarge}, io.Discard); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := run([]string{"version"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, http.DefaultClient, "")
	if exitCode != exitOK {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if got := strings.TrimSpace(stdout.String()); got != version {
		t.Fatalf("unexpected version output: %q", got)
	}
}

func TestRunHelpWithOtherFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"--help", "--repo", "."}, strings.NewReader(""), &stdout, &stderr, http.DefaultClient, "")
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
	exitCode := run([]string{"--repo", repo, "--yes"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL)
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
	exitCode := run([]string{"--repo", repo, "--yes", "--cooldown-days", "7"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL)
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
	exitCode := run([]string{"--repo", repo, "--yes", "--cooldown-days", "7"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL)
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
	exitCode := run([]string{"--repo", repo, "--yes"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL)
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
	exitCode := run([]string{"--repo", repo, "--yes"}, strings.NewReader(""), &stdout, &stderr, server.Client(), server.URL)
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
