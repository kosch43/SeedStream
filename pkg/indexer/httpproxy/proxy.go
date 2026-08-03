package httpproxy

import (
	"net/http"
	"net/url"
	"strings"
)

// IndexerProxy returns http.Transport.Proxy: per-indexer fixed URL if set and valid (http/https), otherwise http.ProxyFromEnvironment.
func IndexerProxy(fixedProxyURL string) func(*http.Request) (*url.URL, error) {
	fixed := strings.TrimSpace(fixedProxyURL)
	if fixed == "" {
		return http.ProxyFromEnvironment
	}
	u, err := url.Parse(fixed)
	if err != nil || u.Host == "" {
		return http.ProxyFromEnvironment
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return http.ProxyFromEnvironment
	}
	canon := *u
	return func(*http.Request) (*url.URL, error) {
		return &canon, nil
	}
}

