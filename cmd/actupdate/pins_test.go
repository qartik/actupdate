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

func TestPinFlagAndHelp(t *testing.T) {
	opts, err := parseArgs([]string{"--pin"})
	if err != nil || !opts.Pin {
		t.Fatalf("opts=%+v, err=%v", opts, err)
	}
	var out bytes.Buffer
	printUsage(&out)
	if !strings.Contains(out.String(), "-pin") {
		t.Fatal("pin flag missing from help")
	}
}

func TestRunPinnedUpdates(t *testing.T) {
	const oldSHA = "1123456789abcdef0123456789abcdef01234567"
	const newSHA = "0123456789abcdef0123456789abcdef01234567"
	const target = newSHA + " # v3.1.0"
	for _, tc := range []struct {
		name, source, want, path, input, output string
		flags                                   []string
		failRef, noExact                        bool
		exit                                    int
	}{
		{name: "convert outdated tag", source: "uses: owner/action@v1\n", want: "uses: owner/action@" + target + "\n", flags: []string{"--pin", "--yes"}, output: target},
		{name: "convert current tag", source: "uses: owner/action@v3.1.0\n", want: "uses: owner/action@" + target + "\n", flags: []string{"--pin", "--yes"}},
		{name: "automatic annotated update", source: "uses: owner/action@" + oldSHA + " # v1.0.0\n", want: "uses: owner/action@" + target + "\n", flags: []string{"--yes"}},
		{name: "moving annotation", source: "uses: owner/action@" + oldSHA + " # v3\n", want: "uses: owner/action@" + target + "\n", flags: []string{"--yes"}},
		{name: "same commit newer version", source: "uses: owner/action@" + newSHA + " # v3.0.0\n", want: "uses: owner/action@" + target + "\n", flags: []string{"--yes"}},
		{name: "unchanged exact pin", source: "uses: owner/action@" + oldSHA + " # v3.1.0\n", flags: []string{"--pin", "--yes"}, output: "0 updates"},
		{name: "no downgrade", source: "uses: owner/action@v4.0.0\n", flags: []string{"--pin", "--yes"}, output: "0 updates"},
		{name: "bare pin skipped", source: "uses: owner/action@" + oldSHA + "\n", flags: []string{"--pin", "--yes"}, output: "without a stable semver comment"},
		{name: "nonleading annotation skipped", source: "uses: owner/action@" + oldSHA + " # explanation v1.0.0\n", flags: []string{"--yes"}, output: "1 skipped"},
		{name: "no exact tags", source: "uses: owner/action@v1\n", flags: []string{"--pin", "--yes"}, noExact: true, output: "no stable exact semver tags"},
		{name: "confirmation declined", source: "uses: owner/action@v1\n", flags: []string{"--pin"}, input: "n\n", output: "Aborted."},
		{name: "confirmation accepted", source: "uses: owner/action@v1\n", want: "uses: owner/action@" + target + "\n", flags: []string{"--pin"}, input: "\n", output: "Applied updates"},
		{name: "all writes prevented", source: "steps:\n  - uses: owner/action@v1\n  - uses: missing/action@v1\n", flags: []string{"--pin", "--yes"}, exit: exitVerificationFailure, output: "1 verification errors"},
		{name: "commit resolution failure", source: "uses: owner/action@v1\n", flags: []string{"--pin", "--yes"}, failRef: true, exit: exitVerificationFailure, output: "verification failed"},
		{name: "composite action", source: "runs:\n  using: composite\n  steps:\n    - uses: owner/action/subpath@v1 # explanation\n", want: "runs:\n  using: composite\n  steps:\n    - uses: owner/action/subpath@" + target + " explanation\n", path: "nested/action.yml", flags: []string{"--pin", "--yes", "--include-composite-actions"}},
		{name: "reusable workflow", source: "jobs:\n  test:\n    uses: owner/action/.github/workflows/test.yml@v1\n", want: "jobs:\n  test:\n    uses: owner/action/.github/workflows/test.yml@" + target + "\n", flags: []string{"--pin", "--yes"}},
		{name: "normal tag behavior", source: "uses: owner/action@v1\n", want: "uses: owner/action@v9\n", flags: []string{"--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			relPath := tc.path
			if relPath == "" {
				relPath = ".github/workflows/test.yml"
			}
			path := filepath.Join(repo, relPath)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/action/tags":
					if tc.noExact {
						fmt.Fprint(w, `[{"name":"v9"}]`)
					} else {
						fmt.Fprint(w, `[{"name":"v9"},{"name":"v3.1.0"},{"name":"v3.0.0"}]`)
					}
				case "/repos/owner/action/git/ref/tags/v3.1.0":
					if tc.failRef {
						http.NotFound(w, r)
					} else {
						fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, newSHA)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			args := append([]string{"--repo", repo}, tc.flags...)
			var out, errOut bytes.Buffer
			code := run(args, strings.NewReader(tc.input), &out, &errOut, server.Client(), server.URL, t.TempDir())
			if code != tc.exit {
				t.Fatalf("exit=%d, want=%d; out=%s err=%s", code, tc.exit, &out, &errOut)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = tc.source
			}
			if string(got) != want {
				t.Fatalf("want %q, got %q", want, got)
			}
			if tc.output != "" && !strings.Contains(out.String(), tc.output) {
				t.Fatalf("missing %q in %s", tc.output, &out)
			}
		})
	}
}
