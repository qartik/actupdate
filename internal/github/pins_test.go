package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qartik/actupdate/internal/actionspec"
)

const pinSHA = "0123456789abcdef0123456789abcdef01234567"
const tagSHA = "1123456789abcdef0123456789abcdef01234567"
const nestedTagSHA = "2123456789abcdef0123456789abcdef01234567"

func TestResolvePinnedExactPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, tags, current, target string
		pinned, skipped             bool
	}{
		{"ignore moving and prerelease", `["v9","v8.3","v7.0.0-rc1","v2.1.0","v2.0.0"]`, "v1", "v2.1.0", false, false},
		{"convert current", `["v2.1.0"]`, "v2.1.0", "v2.1.0", false, false},
		{"upgrade annotated pin", `["v2.1.0"]`, "v2.0.0", "v2.1.0", true, false},
		{"migrate moving annotation", `["v2.0.0"]`, "v2", "v2.0.0", true, false},
		{"same exact pin unchanged", `["v2.1.0"]`, "v2.1.0", "", true, false},
		{"equivalent exact pin unchanged", `["2.1.0"]`, "v2.1.0", "", true, false},
		{"no downgrade", `["v2.1.0"]`, "v3.0.0", "", false, false},
		{"no moving annotation downgrade", `["v2.0.0"]`, "v2.1", "", true, false},
		{"no exact tags", `["v3","v3.1","v4.0.0-beta"]`, "v1", "", false, true},
		{"empty tags", `[]`, "v1", "", false, true},
		{"unprefixed tags", `["2.1.0","2.0.0"]`, "1.0.0", "2.1.0", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var names []string
			if err := json.Unmarshal([]byte(tc.tags), &names); err != nil {
				t.Fatal(err)
			}
			tags := make([]tagResponse, 0, len(names))
			for _, name := range names {
				tags = append(tags, tagResponse{Name: name})
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/action/tags" {
					if err := json.NewEncoder(w).Encode(tags); err != nil {
						t.Error(err)
					}
					return
				}
				calls++
				if tc.target == "" || r.URL.Path != "/repos/owner/action/git/ref/tags/"+tc.target {
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, pinSHA)
			}))
			defer server.Close()
			client := NewClient(server.Client(), server.URL, "").WithCacheDir(t.TempDir())
			current, err := actionspec.ParseStableVersion(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.ResolvePinned(context.Background(), "owner/action", current, tc.pinned, 0)
			if err != nil {
				t.Fatal(err)
			}
			if result.TargetTag != tc.target || result.HasUpgrade != (tc.target != "") || result.Skipped != tc.skipped {
				t.Fatalf("unexpected resolution: %+v", result)
			}
			if result.HasUpgrade && result.TargetRef != pinSHA {
				t.Fatalf("unexpected SHA: %s", result.TargetRef)
			}
			if tc.target == "" && calls != 0 {
				t.Fatal("unchanged pin must not resolve a repointed tag")
			}
		})
	}
}

func TestResolvePinnedCooldownFallbackAndObjectReuse(t *testing.T) {
	now := time.Now().UTC()
	for _, allBlocked := range []bool{false, true} {
		t.Run(fmt.Sprint(allBlocked), func(t *testing.T) {
			calls := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls[r.URL.Path]++
				switch r.URL.Path {
				case "/repos/owner/action/tags":
					fmt.Fprint(w, `[{"name":"v3.0.0"},{"name":"v2.1.0"},{"name":"v2.0.0"}]`)
				case "/repos/owner/action/git/ref/tags/v3.0.0":
					fmt.Fprintf(w, `{"object":{"type":"tag","sha":%q}}`, tagSHA)
				case "/repos/owner/action/git/tags/" + tagSHA:
					fmt.Fprintf(w, `{"tagger":{"date":%q},"object":{"type":"commit","sha":%q}}`, now.Format(time.RFC3339), pinSHA)
				case "/repos/owner/action/git/ref/tags/v2.1.0":
					fmt.Fprintf(w, `{"object":{"type":"tag","sha":%q}}`, nestedTagSHA)
				case "/repos/owner/action/git/tags/" + nestedTagSHA:
					date := now.Add(-10 * 24 * time.Hour)
					if allBlocked {
						date = now
					}
					fmt.Fprintf(w, `{"tagger":{"date":%q},"object":{"type":"commit","sha":%q}}`, date.Format(time.RFC3339), pinSHA)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := NewClient(server.Client(), server.URL, "").WithCacheDir(t.TempDir())
			client.now = func() time.Time { return now }
			current, _ := actionspec.ParseStableVersion("v2.0.0")
			for range 2 {
				result, err := client.ResolvePinned(context.Background(), "owner/action", current, true, 7*24*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if allBlocked {
					if result.HasUpgrade || !strings.Contains(result.Reason, "cooldown") {
						t.Fatalf("unexpected resolution: %+v", result)
					}
				} else if result.TargetTag != "v2.1.0" || result.TargetRef != pinSHA {
					t.Fatalf("unexpected resolution: %+v", result)
				}
			}
			for path, count := range calls {
				if count != 1 {
					t.Errorf("%s requested %d times", path, count)
				}
			}
		})
	}
}

func TestTagCommitObjectsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, ref, outer, inner, errorText string
	}{
		{"lightweight", `{"type":"commit","sha":"` + pinSHA + `"}`, "", "", ""},
		{"annotated", `{"type":"tag","sha":"` + tagSHA + `"}`, `{"object":{"type":"commit","sha":"` + pinSHA + `"}}`, "", ""},
		{"nested", `{"type":"tag","sha":"` + tagSHA + `"}`, `{"object":{"type":"tag","sha":"` + nestedTagSHA + `"}}`, `{"object":{"type":"commit","sha":"` + pinSHA + `"}}`, ""},
		{"cycle", `{"type":"tag","sha":"` + tagSHA + `"}`, `{"object":{"type":"tag","sha":"` + tagSHA + `"}}`, "", "cyclic"},
		{"unsupported", `{"type":"tree","sha":"` + pinSHA + `"}`, "", "", "unsupported"},
		{"short SHA", `{"type":"commit","sha":"0123456"}`, "", "", "invalid full SHA"},
		{"empty SHA", `{"type":"commit"}`, "", "", "invalid full SHA"},
		{"missing tag", "", "", "", "not found"},
		{"missing object", `{"type":"tag","sha":"` + tagSHA + `"}`, "", "", "not found"},
		{"malformed JSON", "broken", "", "", "failed to decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body string
				switch r.URL.Path {
				case "/repos/owner/action/git/ref/tags/v1.0.0":
					if tc.ref != "" {
						body = `{"object":` + tc.ref + `}`
					}
				case "/repos/owner/action/git/tags/" + tagSHA:
					body = tc.outer
				case "/repos/owner/action/git/tags/" + nestedTagSHA:
					body = tc.inner
				}
				if body == "" {
					http.NotFound(w, r)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := NewClient(server.Client(), server.URL, "")
			sha, err := client.tagCommit(context.Background(), "owner/action", "v1.0.0")
			if tc.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("expected %q, got %v", tc.errorText, err)
				}
			} else if err != nil || sha != pinSHA {
				t.Fatalf("SHA=%s, err=%v", sha, err)
			}
		})
	}
}

func TestResolvePinnedLightweightCooldownAndRequestReuse(t *testing.T) {
	for _, age := range []time.Duration{0, 10 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			now := time.Now().UTC()
			calls := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls[r.URL.Path]++
				switch r.URL.Path {
				case "/repos/owner/action/tags":
					fmt.Fprint(w, `[{"name":"v1.0.0"}]`)
				case "/repos/owner/action/git/ref/tags/v1.0.0":
					fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, pinSHA)
				case "/repos/owner/action/git/commits/" + pinSHA:
					fmt.Fprintf(w, `{"committer":{"date":%q}}`, now.Add(-age).Format(time.RFC3339))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := NewClient(server.Client(), server.URL, "").WithCacheDir(t.TempDir())
			client.now = func() time.Time { return now }
			current, _ := actionspec.ParseStableVersion("v1.0.0")
			for range 2 {
				result, err := client.ResolvePinned(context.Background(), "owner/action", current, false, 7*24*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if result.HasUpgrade != (age > 0) {
					t.Fatalf("unexpected conversion eligibility: %+v", result)
				}
			}
			for path, count := range calls {
				if count != 1 {
					t.Errorf("%s requested %d times", path, count)
				}
			}
		})
	}
}
