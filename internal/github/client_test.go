package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestResolveLatestMajorPrefersMovingTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v6"},{"name":"v6.2.1"},{"name":"v5.9.0"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4, 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6" || result.Reason != "moving major tag" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorFallsBackToExactTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v6.2.1"},{"name":"v6.1.0"},{"name":"v5.9.0"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4, 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6.2.1" || result.Reason != "exact tag fallback" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorIgnoresPrerelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v6.0.0-rc1"},{"name":"v5"},{"name":"v4"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 5, 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.HasUpgrade {
		t.Fatalf("expected no upgrade, got %+v", result)
	}
}

func TestResolveLatestMajorHandlesNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	if _, err := client.ResolveLatestMajor(context.Background(), "missing/repo", 4, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveLatestMajorWithCooldownUsesOlderMovingTag(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6", "v6.2.1", "v4"},
		refs: map[string]gitRef{
			"v6":     {Type: "tag", SHA: "tag-v6"},
			"v6.2.1": {Type: "tag", SHA: "tag-v621"},
		},
		tagObjects: map[string]string{
			"tag-v6":   now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
			"tag-v621": now.Add(-9 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6" || result.Reason != "moving major tag" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorWithCooldownFallsBackToOlderExactTag(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6", "v6.2.1", "v4"},
		refs: map[string]gitRef{
			"v6":     {Type: "tag", SHA: "tag-v6"},
			"v6.2.1": {Type: "tag", SHA: "tag-v621"},
		},
		tagObjects: map[string]string{
			"tag-v6":   now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
			"tag-v621": now.Add(-9 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6.2.1" || result.Reason != "exact tag fallback" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorWithCooldownSkipsTooNewMajor(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6", "v6.2.1", "v4"},
		refs: map[string]gitRef{
			"v6":     {Type: "tag", SHA: "tag-v6"},
			"v6.2.1": {Type: "tag", SHA: "tag-v621"},
		},
		tagObjects: map[string]string{
			"tag-v6":   now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
			"tag-v621": now.Add(-1 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.HasUpgrade {
		t.Fatalf("expected no upgrade, got %+v", result)
	}
	if result.Reason != "newer major tags are still within cooldown" {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

func TestResolveLatestMajorCooldownUsesAnnotatedTagTimestamp(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6"},
		refs: map[string]gitRef{
			"v6": {Type: "tag", SHA: "tag-v6"},
		},
		tagObjects: map[string]string{
			"tag-v6": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 5, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorCooldownUsesLightweightCommitTimestamp(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6"},
		refs: map[string]gitRef{
			"v6": {Type: "commit", SHA: "commit-v6"},
		},
		commits: map[string]string{
			"commit-v6": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 5, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorCachesTagTimestamps(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	hits := map[string]int{}
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6"},
		refs: map[string]gitRef{
			"v6": {Type: "tag", SHA: "tag-v6"},
		},
		tagObjects: map[string]string{
			"tag-v6": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		},
		hits: hits,
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	for range 2 {
		result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 5, 7*24*time.Hour)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !result.HasUpgrade || result.TargetRef != "v6" {
			t.Fatalf("unexpected result: %+v", result)
		}
	}

	if hits["/repos/actions/checkout/git/ref/tags/v6"] != 1 {
		t.Fatalf("expected one ref lookup, got %d", hits["/repos/actions/checkout/git/ref/tags/v6"])
	}
	if hits["/repos/actions/checkout/git/tags/tag-v6"] != 1 {
		t.Fatalf("expected one tag-object lookup, got %d", hits["/repos/actions/checkout/git/tags/tag-v6"])
	}
}

func TestResolveLatestMajorEscapesTagPathSegments(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	hits := map[string]int{}
	escapedHits := map[string]int{}
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"release/6", "v4"},
		refs: map[string]gitRef{
			"release/6": {Type: "tag", SHA: "tag-release-6"},
		},
		tagObjects: map[string]string{
			"tag-release-6": now.Add(-10 * 24 * time.Hour).Format(time.RFC3339),
		},
		hits:        hits,
		escapedHits: escapedHits,
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	publishedAt, err := client.tagPublishedAt(context.Background(), "actions/checkout", "release/6")
	if err != nil {
		t.Fatalf("tagPublishedAt: %v", err)
	}
	if publishedAt.IsZero() {
		t.Fatal("expected publish time")
	}
	if escapedHits["/repos/actions/checkout/git/ref/tags/release%2F6"] != 1 {
		t.Fatalf("expected escaped ref lookup, got %#v", escapedHits)
	}
}

type githubTestData struct {
	tags        []string
	refs        map[string]gitRef
	tagObjects  map[string]string
	commits     map[string]string
	hits        map[string]int
	escapedHits map[string]int
}

type gitRef struct {
	Type string
	SHA  string
}

func newGitHubTestServer(t *testing.T, data githubTestData) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data.hits != nil {
			data.hits[r.URL.Path]++
		}
		if data.escapedHits != nil {
			data.escapedHits[r.URL.EscapedPath()]++
		}

		switch {
		case r.URL.Path == "/repos/actions/checkout/tags":
			type tagItem struct {
				Name string `json:"name"`
			}
			items := make([]tagItem, 0, len(data.tags))
			for _, tag := range data.tags {
				items = append(items, tagItem{Name: tag})
			}
			writeJSON(t, w, items)
			return
		case strings.HasPrefix(r.URL.Path, "/repos/actions/checkout/git/ref/tags/"):
			tag := strings.TrimPrefix(r.URL.EscapedPath(), "/repos/actions/checkout/git/ref/tags/")
			unescapedTag, err := url.PathUnescape(tag)
			if err != nil {
				t.Fatalf("unescape tag: %v", err)
			}
			ref, ok := data.refs[unescapedTag]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, map[string]any{
				"object": map[string]any{
					"type": ref.Type,
					"sha":  ref.SHA,
				},
			})
			return
		case strings.HasPrefix(r.URL.Path, "/repos/actions/checkout/git/tags/"):
			sha := strings.TrimPrefix(r.URL.Path, "/repos/actions/checkout/git/tags/")
			date, ok := data.tagObjects[sha]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, map[string]any{
				"tagger": map[string]any{
					"date": date,
				},
			})
			return
		case strings.HasPrefix(r.URL.Path, "/repos/actions/checkout/git/commits/"):
			sha := strings.TrimPrefix(r.URL.Path, "/repos/actions/checkout/git/commits/")
			date, ok := data.commits[sha]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, map[string]any{
				"committer": map[string]any{
					"date": date,
				},
			})
			return
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}
