package httpproxy

import (
	"net/http"
	"net/url"
	"testing"
)

func TestIndexerProxy_fixedURL(t *testing.T) {
	fn := IndexerProxy("http://proxy:8888")
	u, err := fn(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.String() != "http://proxy:8888" {
		t.Fatalf("got %v", u)
	}
}

func TestIndexerProxy_environmentFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy:3128")
	t.Setenv("HTTPS_PROXY", "http://env-proxy:3128")
	fn := IndexerProxy("")
	u, err := fn(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "env-proxy:3128" {
		t.Fatalf("got %v", u)
	}
}

