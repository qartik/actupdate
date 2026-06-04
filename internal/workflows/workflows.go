package workflows

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var usesPattern = regexp.MustCompile(`^(\s*(?:-\s*)?uses:\s*)(?:"([^"\n#]+)"|'([^'\n#]+)'|([^"'#\n]+))(\s*(?:#.*)?)$`)
var validateYAML = func(content []byte) error {
	var out any
	return yaml.Unmarshal(content, &out)
}

type FileScan struct {
	Path    string
	Matches []Match
}

type Match struct {
	FilePath string
	Value    string
	Line     int
	Start    int
	End      int
}

type Change struct {
	Match  Match
	NewRef string
}

type DiscoverOptions struct {
	IncludeCompositeActions bool
}

type InvalidYAMLError struct {
	Path string
	Err  error
}

func (e *InvalidYAMLError) Error() string {
	return fmt.Sprintf("%s: %v", e.Path, e.Err)
}

func (e *InvalidYAMLError) Unwrap() error {
	return e.Err
}

func Discover(repoRoot string, opts DiscoverOptions) ([]string, error) {
	info, err := os.Stat(repoRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", repoRoot)
	}

	patterns := []string{
		filepath.Join(repoRoot, ".github", "workflows", "*.yml"),
		filepath.Join(repoRoot, ".github", "workflows", "*.yaml"),
	}
	seen := map[string]struct{}{}
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			files = append(files, match)
		}
	}
	if opts.IncludeCompositeActions {
		if err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if name != "action.yml" && name != "action.yaml" {
				return nil
			}
			if _, ok := seen[path]; ok {
				return nil
			}
			seen[path] = struct{}{}
			files = append(files, path)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func ScanFiles(repoRoot string, files []string) ([]FileScan, error) {
	scans := make([]FileScan, 0, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		matches := scanContent(path, content)
		relPath, err := filepath.Rel(repoRoot, path)
		if err != nil {
			relPath = path
		}
		for i := range matches {
			matches[i].FilePath = relPath
		}
		scans = append(scans, FileScan{Path: path, Matches: matches})
	}
	return scans, nil
}

func scanContent(path string, content []byte) []Match {
	lines := strings.Split(string(content), "\n")
	var matches []Match
	offset := 0
	for idx, line := range lines {
		submatches := usesPattern.FindStringSubmatchIndex(line)
		if submatches != nil {
			valueStart, valueEnd := firstDefinedCapture(submatches, 4, 6, 8)
			matches = append(matches, Match{
				FilePath: path,
				Value:    line[valueStart:valueEnd],
				Line:     idx + 1,
				Start:    offset + valueStart,
				End:      offset + valueEnd,
			})
		}
		offset += len(line) + 1
	}
	return matches
}

func firstDefinedCapture(submatches []int, indices ...int) (int, int) {
	for _, index := range indices {
		if index+1 < len(submatches) && submatches[index] != -1 && submatches[index+1] != -1 {
			return submatches[index], submatches[index+1]
		}
	}
	return -1, -1
}

func Apply(repoRoot string, changes []Change) error {
	if len(changes) == 0 {
		return nil
	}

	byFile := map[string][]Change{}
	for _, change := range changes {
		path := filepath.Join(repoRoot, change.Match.FilePath)
		byFile[path] = append(byFile[path], change)
	}

	originals := map[string][]byte{}
	written := map[string]struct{}{}

	for path, fileChanges := range byFile {
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		originals[path] = original
		sort.Slice(fileChanges, func(i, j int) bool {
			return fileChanges[i].Match.Start > fileChanges[j].Match.Start
		})

		updated := append([]byte(nil), original...)
		for _, change := range fileChanges {
			if change.Match.Start < 0 || change.Match.End > len(updated) || change.Match.Start > change.Match.End {
				return fmt.Errorf("%s: invalid replacement range", change.Match.FilePath)
			}
			oldRef := string(updated[change.Match.Start:change.Match.End])
			replaced := replaceRef(oldRef, change.NewRef)
			updated = splice(updated, change.Match.Start, change.Match.End, []byte(replaced))
		}

		if err := writeAtomically(path, updated); err != nil {
			restoreFiles(originals, written)
			return err
		}
		written[path] = struct{}{}

		rewritten, err := os.ReadFile(path)
		if err != nil {
			restoreFiles(originals, written)
			return err
		}
		if err := validateYAML(rewritten); err != nil {
			restoreFiles(originals, written)
			return &InvalidYAMLError{Path: path, Err: err}
		}
	}

	return nil
}

func replaceRef(value string, newRef string) string {
	trimmed := strings.TrimRight(value, " \t")
	trailing := value[len(trimmed):]
	at := strings.LastIndex(trimmed, "@")
	if at == -1 {
		return value
	}
	return trimmed[:at+1] + newRef + trailing
}

func splice(content []byte, start, end int, replacement []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(content) - (end - start) + len(replacement))
	out.Write(content[:start])
	out.Write(replacement)
	out.Write(content[end:])
	return out.Bytes()
}

func writeAtomically(path string, content []byte) error {
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func restoreFiles(originals map[string][]byte, written map[string]struct{}) {
	for path := range written {
		_ = os.WriteFile(path, originals[path], 0o644)
	}
}
