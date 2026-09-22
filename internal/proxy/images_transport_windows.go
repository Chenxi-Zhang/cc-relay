//go:build windows

package proxy

import (
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const windowsInternetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// staticHTTPSProxy reads the enabled Windows static proxy from the registry.
// Returns nil when no proxy is configured.
func staticHTTPSProxy() (*url.URL, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, windowsInternetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return nil, nil
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return nil, nil
	}
	proxyAddress := selectHTTPSProxy(server)
	if proxyAddress == "" {
		return nil, nil
	}
	if !strings.Contains(proxyAddress, "://") {
		proxyAddress = "http://" + proxyAddress
	}
	proxyURL, err := url.Parse(proxyAddress)
	if err != nil || proxyURL.Host == "" {
		return nil, nil
	}
	return proxyURL, nil
}

// Windows ProxyServer is either one proxy for every protocol (host:port), or
// a semicolon-separated map such as "http=host:port;https=host:port".
func selectHTTPSProxy(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "=") {
		return value
	}
	proxies := make(map[string]string)
	for _, entry := range strings.Split(value, ";") {
		protocol, address, ok := strings.Cut(entry, "=")
		if ok {
			proxies[strings.ToLower(strings.TrimSpace(protocol))] = strings.TrimSpace(address)
		}
	}
	if proxies["https"] != "" {
		return proxies["https"]
	}
	return proxies["http"]
}
