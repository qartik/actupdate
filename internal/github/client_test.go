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

	"actupdate/internal/actionspec"
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

func TestResolveLatestStableUpdatesWithinCurrentMajor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v3.3"},{"name":"v3.0"},{"name":"v2.9"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestStable(context.Background(), "pypa/cibuildwheel", mustParseStableVersion(t, "v3.0"), 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v3.3" || result.Reason != "newer stable version in current major" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestStableLeavesCurrentMajorMovingTagUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v3.3"},{"name":"v3"},{"name":"v2.9"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestStable(context.Background(), "pypa/cibuildwheel", mustParseStableVersion(t, "v3"), 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.HasUpgrade {
		t.Fatalf("expected no upgrade, got %+v", result)
	}
	if result.Reason != "already on latest stable version" {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

func TestResolveLatestStableReportsPublishedLatestMajor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v6.2.1"},{"name":"v6"},{"name":"v5.9"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestStable(context.Background(), "actions/checkout", mustParseStableVersion(t, "v99.0.0"), 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.HasUpgrade {
		t.Fatalf("expected no upgrade, got %+v", result)
	}
	if result.LatestMajor != 6 {
		t.Fatalf("expected published latest major 6, got %d", result.LatestMajor)
	}
}

func TestResolveLatestStableTreatsEquivalentExactTagsAsUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v3.0"},{"name":"v3.0.0"},{"name":"v2.9"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestStable(context.Background(), "pypa/cibuildwheel", mustParseStableVersion(t, "v3.0.0"), 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.HasUpgrade {
		t.Fatalf("expected no upgrade, got %+v", result)
	}
	if result.TargetRef != "" {
		t.Fatalf("expected no target ref, got %q", result.TargetRef)
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

func TestResolveLatestMajorWithCooldownFallsBackToExactTagWhenMovingTagBlocked(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6", "v6.0.0", "v4"},
		refs: map[string]gitRef{
			"v6":     {Type: "tag", SHA: "tag-v6"},
			"v6.0.0": {Type: "tag", SHA: "tag-v600"},
		},
		tagObjects: map[string]string{
			"tag-v6":   now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
			"tag-v600": now.Add(-9 * 24 * time.Hour).Format(time.RFC3339),
		},
	})
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	client.now = func() time.Time { return now }

	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v6.0.0" || result.Reason != "exact tag fallback" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestResolveLatestMajorWithCooldownSkipsMovingOnlyBlockedMajor(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v7", "v6.2.1", "v4"},
		refs: map[string]gitRef{
			"v7":     {Type: "tag", SHA: "tag-v7"},
			"v6.2.1": {Type: "tag", SHA: "tag-v621"},
		},
		tagObjects: map[string]string{
			"tag-v7":   now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
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

func TestResolveLatestMajorWithCooldownMovingOnlyBlockedMajorReturnsCooldown(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	server := newGitHubTestServer(t, githubTestData{
		tags: []string{"v6", "v4"},
		refs: map[string]gitRef{
			"v6": {Type: "tag", SHA: "tag-v6"},
		},
		tagObjects: map[string]string{
			"tag-v6": now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
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

func TestResolvePoliciesMatchReferenceModel(t *testing.T) {
	for currentMajor := 1; currentMajor <= 3; currentMajor++ {
		for currentKind := 0; currentKind < 2; currentKind++ {
			for currentState := 0; currentState <= 8; currentState++ {
				for newerState := 0; newerState <= 8; newerState++ {
					for newestState := 0; newestState <= 8; newestState++ {
						candidates := makePolicyCandidates(currentMajor, currentState, newerState, newestState)
						current := makeCurrentVersion(currentMajor, currentKind)

						gotMajor := resolveLatestMajorPolicy(currentMajor, candidates)
						wantMajor := resolveLatestMajorPolicyModel(currentMajor, candidates)
						if gotMajor != wantMajor {
							t.Fatalf("major policy mismatch currentMajor=%d currentState=%d newerState=%d newestState=%d got=%+v want=%+v",
								currentMajor, currentState, newerState, newestState, gotMajor, wantMajor)
						}

						gotStable := resolveLatestStablePolicy(current, candidates)
						wantStable := resolveLatestStablePolicyModel(current, candidates)
						if gotStable != wantStable {
							t.Fatalf("stable policy mismatch current=%+v currentState=%d newerState=%d newestState=%d got=%+v want=%+v",
								current, currentState, newerState, newestState, gotStable, wantStable)
						}
					}
				}
			}
		}
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

func mustParseStableVersion(t *testing.T, ref string) actionspec.StableVersion {
	if t != nil {
		t.Helper()
	}
	version, err := actionspec.ParseStableVersion(ref)
	if err != nil {
		if t != nil {
			t.Fatalf("parse stable version %q: %v", ref, err)
		}
		panic(fmt.Sprintf("parse stable version %q: %v", ref, err))
	}
	return version
}

func makePolicyCandidates(currentMajor, currentState, newerState, newestState int) []majorCandidates {
	states := []struct {
		major int
		state int
	}{
		{currentMajor + 2, newestState},
		{currentMajor + 1, newerState},
		{currentMajor, currentState},
	}
	candidates := make([]majorCandidates, 0, len(states))
	for _, item := range states {
		candidate, ok := candidateFromState(item.major, item.state)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func candidateFromState(major, state int) (majorCandidates, bool) {
	candidate := majorCandidates{Major: major}
	switch state {
	case 0:
		return majorCandidates{}, false
	case 1:
		candidate.Moving = mustParseStableVersion(nil, fmt.Sprintf("v%d", major))
		candidate.HasMoving = true
		candidate.MovingEligible = true
	case 2:
		candidate.Moving = mustParseStableVersion(nil, fmt.Sprintf("v%d", major))
		candidate.HasMoving = true
	case 3:
		candidate.Exact = mustParseStableVersion(nil, fmt.Sprintf("v%d.2.0", major))
		candidate.HasExact = true
		candidate.ExactEligible = true
	case 4:
		candidate.Exact = mustParseStableVersion(nil, fmt.Sprintf("v%d.2.0", major))
		candidate.HasExact = true
	case 5:
		candidate.Moving = mustParseStableVersion(nil, fmt.Sprintf("v%d", major))
		candidate.HasMoving = true
		candidate.MovingEligible = true
		candidate.Exact = mustParseStableVersion(nil, fmt.Sprintf("v%d.2.0", major))
		candidate.HasExact = true
		candidate.ExactEligible = true
	case 6:
		candidate.Moving = mustParseStableVersion(nil, fmt.Sprintf("v%d", major))
		candidate.HasMoving = true
		candidate.Exact = mustParseStableVersion(nil, fmt.Sprintf("v%d.2.0", major))
		candidate.HasExact = true
		candidate.ExactEligible = true
	case 7:
		candidate.Moving = mustParseStableVersion(nil, fmt.Sprintf("v%d", major))
		candidate.HasMoving = true
		candidate.Exact = mustParseStableVersion(nil, fmt.Sprintf("v%d.2.0", major))
		candidate.HasExact = true
	case 8:
		candidate.Moving = mustParseStableVersion(nil, fmt.Sprintf("v%d", major))
		candidate.HasMoving = true
		candidate.MovingEligible = true
		candidate.Exact = mustParseStableVersion(nil, fmt.Sprintf("v%d.2.0", major))
		candidate.HasExact = true
	default:
		panic(fmt.Sprintf("unknown state %d", state))
	}
	return candidate, true
}

func makeCurrentVersion(currentMajor, currentKind int) actionspec.StableVersion {
	if currentKind == 0 {
		return mustParseStableVersion(nil, fmt.Sprintf("v%d", currentMajor))
	}
	return mustParseStableVersion(nil, fmt.Sprintf("v%d.1.0", currentMajor))
}

func resolveLatestMajorPolicyModel(currentMajor int, candidates []majorCandidates) Resolution {
	latestMajor := latestPublishedMajorFromCandidates(candidates)
	if latestMajor <= currentMajor {
		return Resolution{LatestMajor: latestMajor, Reason: "already on latest stable major"}
	}
	blocked := false
	for _, candidate := range candidates {
		if candidate.Major <= currentMajor {
			continue
		}
		if candidate.HasMoving {
			if candidate.MovingEligible {
				return Resolution{TargetRef: candidate.Moving.Original, HasUpgrade: true, Reason: "moving major tag", LatestMajor: candidate.Major}
			}
			blocked = true
		}
		if candidate.HasExact {
			if candidate.ExactEligible {
				return Resolution{TargetRef: candidate.Exact.Original, HasUpgrade: true, Reason: "exact tag fallback", LatestMajor: candidate.Major}
			}
			blocked = true
		}
	}
	reason := "already on latest stable major"
	if latestMajor > currentMajor || blocked {
		reason = "newer major tags are still within cooldown"
	}
	return Resolution{LatestMajor: latestMajor, Reason: reason}
}

func resolveLatestStablePolicyModel(current actionspec.StableVersion, candidates []majorCandidates) Resolution {
	latestMajor := latestPublishedMajorFromCandidates(candidates)
	blockedNewer := false
	currentExact, hasCurrentExact, currentExactEligible := actionspec.StableVersion{}, false, false
	for _, candidate := range candidates {
		if candidate.Major == current.Major && candidate.HasExact {
			currentExact = candidate.Exact
			hasCurrentExact = true
			currentExactEligible = candidate.ExactEligible
		}
		if candidate.Major <= current.Major {
			continue
		}
		if candidate.HasMoving {
			if candidate.MovingEligible {
				return Resolution{TargetRef: candidate.Moving.Original, HasUpgrade: true, Reason: "moving major tag", LatestMajor: candidate.Major}
			}
			blockedNewer = true
		}
		if candidate.HasExact {
			if candidate.ExactEligible {
				return Resolution{TargetRef: candidate.Exact.Original, HasUpgrade: true, Reason: "exact tag fallback", LatestMajor: candidate.Major}
			}
			blockedNewer = true
		}
	}
	if hasCurrentExact && isSameMajorExactUpgradeModel(current, currentExact) {
		if currentExactEligible {
			return Resolution{TargetRef: currentExact.Original, HasUpgrade: true, Reason: "newer stable version in current major", LatestMajor: latestMajor}
		}
		return Resolution{LatestMajor: latestMajor, Reason: "newer stable tags in current major are still within cooldown"}
	}
	if blockedNewer {
		return Resolution{LatestMajor: latestMajor, Reason: "newer major tags are still within cooldown"}
	}
	return Resolution{LatestMajor: latestMajor, Reason: "already on latest stable version"}
}

func isSameMajorExactUpgradeModel(current, candidate actionspec.StableVersion) bool {
	if current.Major != candidate.Major {
		return false
	}
	if isMovingMajor(current) || isMovingMajor(candidate) {
		return false
	}
	if current.Minor != candidate.Minor {
		return current.Minor < candidate.Minor
	}
	if current.Patch != candidate.Patch {
		return current.Patch < candidate.Patch
	}
	return false
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
