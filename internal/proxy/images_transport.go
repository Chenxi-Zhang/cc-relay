package proxy

import (
	"net/http"
	"net/url"
)

// newCodexTransport builds the HTTP transport used for Codex backend calls.
// It keeps Go's environment proxy behavior and falls back to the user's
// enabled static OS proxy. This matters when the server is launched from a
// GUI (e.g. cc-relay-ui) that does not inherit proxy environment variables.
func newCodexTransport() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		envProxy, err := http.ProxyFromEnvironment(req)
		if err != nil || envProxy != nil {
			return envProxy, err
		}
		return staticHTTPSProxy()
	}
	// SSE must not be transparently gzip-wrapped: a truncated gzip stream
	// surfaces as unexpected EOF and loses already-delivered images.
	transport.DisableCompression = true
	return transport
}
