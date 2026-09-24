package github

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestTLS12HTTPClientClonesDefaultTransport(t *testing.T) {
	client := &http.Client{Timeout: 3 * time.Second}

	got, err := TLS12HTTPClient(client)
	if err != nil {
		t.Fatalf("configure TLS 1.2: %v", err)
	}
	if got == client {
		t.Fatal("expected a cloned HTTP client")
	}
	if got.Timeout != client.Timeout {
		t.Fatalf("expected timeout %v, got %v", client.Timeout, got.Timeout)
	}
	transport, ok := got.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", got.Transport)
	}
	if transport == http.DefaultTransport {
		t.Fatal("expected the default transport to be cloned")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MaxVersion != tls.VersionTLS12 {
		t.Fatalf("expected TLS maximum 1.2, got %#v", transport.TLSClientConfig)
	}
	if client.Transport != nil {
		t.Fatal("expected original client transport to remain unchanged")
	}
}

func TestTLS12HTTPClientPreservesTransportAndTLSSettings(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.example")
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	originalTLS := &tls.Config{
		MinVersion: tls.VersionTLS11,
		ServerName: "api.github.com",
	}
	originalTransport := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: originalTLS,
		MaxIdleConns:    17,
	}
	originalClient := &http.Client{
		Transport: originalTransport,
		Timeout:   5 * time.Second,
	}

	got, err := TLS12HTTPClient(originalClient)
	if err != nil {
		t.Fatalf("configure TLS 1.2: %v", err)
	}
	transport := got.Transport.(*http.Transport)
	if transport == originalTransport {
		t.Fatal("expected transport to be cloned")
	}
	if transport.TLSClientConfig == originalTLS {
		t.Fatal("expected TLS config to be cloned")
	}
	if transport.TLSClientConfig.MaxVersion != tls.VersionTLS12 {
		t.Fatalf("expected TLS maximum 1.2, got %d", transport.TLSClientConfig.MaxVersion)
	}
	if transport.TLSClientConfig.MinVersion != originalTLS.MinVersion || transport.TLSClientConfig.ServerName != originalTLS.ServerName {
		t.Fatalf("TLS settings not preserved: got %#v, want based on %#v", transport.TLSClientConfig, originalTLS)
	}
	if transport.MaxIdleConns != originalTransport.MaxIdleConns {
		t.Fatalf("expected MaxIdleConns %d, got %d", originalTransport.MaxIdleConns, transport.MaxIdleConns)
	}
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "api.github.com"}}
	wantProxy, err := originalTransport.Proxy(request)
	if err != nil {
		t.Fatalf("original proxy: %v", err)
	}
	gotProxy, err := transport.Proxy(request)
	if err != nil {
		t.Fatalf("cloned proxy: %v", err)
	}
	if gotProxy.String() != wantProxy.String() {
		t.Fatalf("expected proxy %q, got %q", wantProxy, gotProxy)
	}
	if originalTLS.MaxVersion != 0 {
		t.Fatalf("expected original TLS maximum to remain unchanged, got %d", originalTLS.MaxVersion)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("expected certificate validation to remain enabled")
	}
}

func TestTLS12HTTPClientSupportsGitHubHeaders(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("expected bearer token, got %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "actupdate" {
			t.Errorf("expected actupdate User-Agent, got %q", got)
		}
		fmt.Fprint(w, `[{"name":"v2"},{"name":"v1"}]`)
	}))
	defer server.Close()

	httpClient, err := TLS12HTTPClient(server.Client())
	if err != nil {
		t.Fatalf("configure TLS 1.2: %v", err)
	}
	client := NewClient(httpClient, server.URL, "test-token")
	result, err := client.ResolveLatestMajor(context.Background(), "owner/action", 1, 0)
	if err != nil {
		t.Fatalf("resolve latest major: %v", err)
	}
	if !result.HasUpgrade || result.TargetRef != "v2" {
		t.Fatalf("unexpected resolution: %+v", result)
	}
}

func TestTLS12HTTPClientRejectsCustomRoundTripper(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	})}
	if _, err := TLS12HTTPClient(client); err == nil {
		t.Fatal("expected custom transport error")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
