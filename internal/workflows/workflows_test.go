package workflows

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFilesFindsUsesReferences(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflow := filepath.Join(path, "test.yml")
	content := strings.Join([]string{
		"jobs:",
		"  test:",
		"    steps:",
		"      - uses: actions/checkout@v4",
		"      - uses: './local-action'",
		`      - uses: "actions/setup-python@v5" # inline`,
		"",
	}, "\n")
	if err := os.WriteFile(workflow, []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	files, err := Discover(repo, DiscoverOptions{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	scans, err := ScanFiles(repo, files)
	if err != nil {
		t.Fatalf("scan files: %v", err)
	}
	if len(scans) != 1 || len(scans[0].Matches) != 3 {
		t.Fatalf("unexpected scan result: %+v", scans)
	}
}

func TestDiscoverDefaultFindsOnlyWorkflowFiles(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir workflow dir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "release.yml")
	if err := os.WriteFile(workflowPath, []byte("steps:\n"), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	compositeDir := filepath.Join(repo, "py-release-checks")
	if err := os.MkdirAll(compositeDir, 0o755); err != nil {
		t.Fatalf("mkdir composite dir: %v", err)
	}
	compositePath := filepath.Join(compositeDir, "action.yml")
	if err := os.WriteFile(compositePath, []byte("runs:\n"), 0o644); err != nil {
		t.Fatalf("write composite action: %v", err)
	}

	files, err := Discover(repo, DiscoverOptions{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 || files[0] != workflowPath {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestDiscoverIncludeCompositeActionsFindsNestedActionMetadata(t *testing.T) {
	repo := t.TempDir()
	workflowDir := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir workflow dir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "release.yml")
	if err := os.WriteFile(workflowPath, []byte("steps:\n"), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	compositeDir := filepath.Join(repo, "py-release-checks")
	if err := os.MkdirAll(compositeDir, 0o755); err != nil {
		t.Fatalf("mkdir composite dir: %v", err)
	}
	compositeYML := filepath.Join(compositeDir, "action.yml")
	if err := os.WriteFile(compositeYML, []byte("runs:\n"), 0o644); err != nil {
		t.Fatalf("write composite yml: %v", err)
	}
	compositeYAML := filepath.Join(repo, "rs-release-checks", "action.yaml")
	if err := os.MkdirAll(filepath.Dir(compositeYAML), 0o755); err != nil {
		t.Fatalf("mkdir composite yaml dir: %v", err)
	}
	if err := os.WriteFile(compositeYAML, []byte("runs:\n"), 0o644); err != nil {
		t.Fatalf("write composite yaml: %v", err)
	}
	unrelated := filepath.Join(repo, "misc", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(unrelated), 0o755); err != nil {
		t.Fatalf("mkdir unrelated dir: %v", err)
	}
	if err := os.WriteFile(unrelated, []byte("name: test\n"), 0o644); err != nil {
		t.Fatalf("write unrelated yaml: %v", err)
	}
	gitAction := filepath.Join(repo, ".git", "actions", "action.yml")
	if err := os.MkdirAll(filepath.Dir(gitAction), 0o755); err != nil {
		t.Fatalf("mkdir .git action dir: %v", err)
	}
	if err := os.WriteFile(gitAction, []byte("runs:\n"), 0o644); err != nil {
		t.Fatalf("write .git action: %v", err)
	}

	files, err := Discover(repo, DiscoverOptions{IncludeCompositeActions: true})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	want := []string{workflowPath, compositeYML, compositeYAML}
	if len(files) != len(want) {
		t.Fatalf("unexpected file count: got %v want %v", files, want)
	}
	for i, path := range want {
		if files[i] != path {
			t.Fatalf("unexpected files: got %v want %v", files, want)
		}
	}
}

func TestApplyPreservesOtherContent(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflow := filepath.Join(path, "test.yml")
	content := "steps:\n  - uses: actions/checkout@v4 # comment\n"
	if err := os.WriteFile(workflow, []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	scans, err := ScanFiles(repo, []string{workflow})
	if err != nil {
		t.Fatalf("scan files: %v", err)
	}
	change := Change{Match: scans[0].Matches[0], NewRef: "v6"}
	if err := Apply(repo, []Change{change}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	updated, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	got := string(updated)
	if !strings.Contains(got, "actions/checkout@v6 # comment") {
		t.Fatalf("expected updated content, got %q", got)
	}
}

func TestApplyRollsBackOnInvalidYAML(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workflow := filepath.Join(path, "test.yml")
	content := "steps:\n  - uses: actions/checkout@v4\n"
	if err := os.WriteFile(workflow, []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	scans, err := ScanFiles(repo, []string{workflow})
	if err != nil {
		t.Fatalf("scan files: %v", err)
	}

	originalValidate := validateYAML
	defer func() { validateYAML = originalValidate }()
	validateYAML = func(content []byte) error {
		return os.ErrInvalid
	}

	err = Apply(repo, []Change{{Match: scans[0].Matches[0], NewRef: "v6"}})
	if err == nil {
		t.Fatal("expected error")
	}
	var invalidErr *InvalidYAMLError
	if !errors.As(err, &invalidErr) {
		t.Fatalf("expected InvalidYAMLError, got %T", err)
	}
	if invalidErr.Path != workflow {
		t.Fatalf("expected path %q, got %q", workflow, invalidErr.Path)
	}

	updated, readErr := os.ReadFile(workflow)
	if readErr != nil {
		t.Fatalf("read workflow: %v", readErr)
	}
	if string(updated) != content {
		t.Fatalf("expected rollback, got %q", string(updated))
	}
}
