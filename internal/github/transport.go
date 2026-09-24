package github

import (
	"crypto/tls"
	"fmt"
	"net/http"
)

// TLS12HTTPClient returns a copy of client whose transport caps TLS at 1.2.
// The client, transport, and TLS configuration passed by the caller are not
// modified.
func TLS12HTTPClient(client *http.Client) (*http.Client, error) {
	if client == nil {
		client = http.DefaultClient
	}

	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	baseTransport, ok := transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("HTTP transport %T cannot be configured for TLS", transport)
	}

	clonedTransport := baseTransport.Clone()
	if clonedTransport.TLSClientConfig == nil {
		clonedTransport.TLSClientConfig = &tls.Config{}
	} else {
		clonedTransport.TLSClientConfig = clonedTransport.TLSClientConfig.Clone()
	}
	clonedTransport.TLSClientConfig.MaxVersion = tls.VersionTLS12

	clonedClient := *client
	clonedClient.Transport = clonedTransport
	return &clonedClient, nil
}
