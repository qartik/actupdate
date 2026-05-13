package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
