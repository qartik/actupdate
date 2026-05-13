package workflows

import (
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

	files, err := Discover(repo)
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

	updated, readErr := os.ReadFile(workflow)
	if readErr != nil {
		t.Fatalf("read workflow: %v", readErr)
	}
	if string(updated) != content {
		t.Fatalf("expected rollback, got %q", string(updated))
	}
}
