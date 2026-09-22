//go:build !windows

package proxy

import "net/url"

// staticHTTPSProxy is a no-op on non-Windows platforms; only environment
// proxies apply there.
func staticHTTPSProxy() (*url.URL, error) {
	return nil, nil
}
