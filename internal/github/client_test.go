package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveLatestMajorPrefersMovingTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"v6"},{"name":"v6.2.1"},{"name":"v5.9.0"}]`)
	}))
	defer server.Close()

	client := NewClient(server.Client(), server.URL, "")
	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4)
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
	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 4)
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
	result, err := client.ResolveLatestMajor(context.Background(), "actions/checkout", 5)
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
	if _, err := client.ResolveLatestMajor(context.Background(), "missing/repo", 4); err == nil {
		t.Fatal("expected error")
	}
}
