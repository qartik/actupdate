package workflows

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestScanRequiresSeparatedComments(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name, reference, suffix string
		wantMatch               bool
	}{
		{"unseparated SHA", "owner/action@" + sha, "# v1.0.0", false},
		{"unseparated tag", "owner/action@v1", "# v1.0.0", false},
		{"space separated", "owner/action@" + sha, " # v1.0.0", true},
		{"tab separated", "owner/action@" + sha, "\t# v1.0.0", true},
		{"quoted separated", "\"owner/action@" + sha + "\"", " # v1.0.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := "uses: " + tc.reference + tc.suffix
			var parsed map[string]string
			if err := yaml.Unmarshal([]byte(line), &parsed); err != nil {
				t.Fatal(err)
			}
			if !tc.wantMatch && parsed["uses"] != tc.reference+tc.suffix {
				t.Fatalf("expected hash inside scalar, got %q", parsed["uses"])
			}
			// Ensure skipping the first line does not corrupt later byte offsets.
			content := line + "\r\nuses: owner/action@v2 # v2.0.0\r\n"
			matches := scanContent("test.yml", []byte(content))
			wantCount := 1
			if tc.wantMatch {
				wantCount++
			}
			if len(matches) != wantCount {
				t.Fatalf("unexpected matches: %+v", matches)
			}
			if tc.wantMatch && matches[0].VersionHint != "v1.0.0" {
				t.Fatalf("lost annotation: %+v", matches[0])
			}
			last := matches[len(matches)-1]
			if last.Line != 2 || content[last.Start:last.End] != "owner/action@v2 " || last.VersionHint != "v2.0.0" {
				t.Fatalf("invalid subsequent match: %+v", last)
			}
		})
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

func TestApplyPinsPreserveFormatting(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct{ name, original, want string }{
		{"plain", "  - uses: owner/action@v1\n", "  - uses: owner/action@" + sha + " # v2.1.0\n"},
		{"quoted", "  - uses: 'owner/action/path@v1'  # explanation\n", "  - uses: 'owner/action/path@" + sha + "'  # v2.1.0 explanation\n"},
		{"annotated", "  - uses: \"owner/action@" + sha + "\" # v1.0.0 keep this\n", "  - uses: \"owner/action@" + sha + "\" # v2.1.0 keep this\n"},
		{"no comment space", "  - uses: 'owner/action@v1' #explanation\n", "  - uses: 'owner/action@" + sha + "' # v2.1.0 explanation\n"},
		{"CRLF multiple replacements", "  - uses: owner/action@v1\r\n  - uses: owner/action/.github/workflows/test.yml@v1 # v1.0.0 notes\r\n", "  - uses: owner/action@" + sha + " # v2.1.0\r\n  - uses: owner/action/.github/workflows/test.yml@" + sha + " # v2.1.0 notes\r\n"},
		{"no final newline", "  - uses: owner/action@v1", "  - uses: owner/action@" + sha + " # v2.1.0"},
		{"tabs and trailing whitespace", "  - uses: \"owner/action@v1\"\t#\tv1.0.0\tcomment  \n", "  - uses: \"owner/action@" + sha + "\"\t#\tv2.1.0\tcomment  \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, "test.yml")
			if err := os.WriteFile(path, []byte(tc.original), 0o644); err != nil {
				t.Fatal(err)
			}
			scans, err := ScanFiles(repo, []string{path})
			if err != nil {
				t.Fatal(err)
			}
			if len(scans[0].Matches) == 0 {
				t.Fatal("no matches")
			}
			var changes []Change
			for _, match := range scans[0].Matches {
				changes = append(changes, Change{Match: match, NewRef: sha, NewTag: "v2.1.0"})
			}
			if err := Apply(repo, changes); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestScanVersionHints(t *testing.T) {
	for _, tc := range []struct{ comment, hint string }{
		{"# v1.2.3 explanation", "v1.2.3"},
		{"# 1.2.3", "1.2.3"},
		{"# v1", "v1"},
		{"# v1.2", "v1.2"},
		{"# v1.2.3-rc1", ""},
		{"# explanation v1.2.3", ""},
		{"# v1.2.3, explanation", ""},
		{"#", ""},
	} {
		matches := scanContent("test.yml", []byte("uses: owner/action@v1 "+tc.comment+"\n"))
		if len(matches) != 1 || matches[0].VersionHint != tc.hint {
			t.Errorf("%q: unexpected matches %+v", tc.comment, matches)
		}
	}
}

func TestApplyPinsRollsBackInvalidYAML(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "test.yml")
	original := "uses: owner/action@v1 # explanation\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	scans, err := ScanFiles(repo, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	// A multiline annotation must fail YAML validation and restore the original.
	err = Apply(repo, []Change{{Match: scans[0].Matches[0], NewRef: "0123456789abcdef0123456789abcdef01234567", NewTag: "v2.0.0\ninvalid: ["}})
	var invalid *InvalidYAMLError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected invalid YAML, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("rollback failed: %q", got)
	}
}
