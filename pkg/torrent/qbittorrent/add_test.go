package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAddRequestsImmediateStreamingSetup(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse add form: %v", err)
			return
		}
		got = r.PostForm
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(Options{BaseURL: srv.URL, Category: "seedstream", SavePath: "/downloads"})
	if err := c.Add(context.Background(), AddOptions{
		URL:        "magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Sequential: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := map[string]string{
		"category":           "seedstream",
		"savepath":           "/downloads",
		"autoTMM":            "false",
		"paused":             "false",
		"stopped":            "false",
		"addToTopOfQueue":    "true",
		"stopCondition":      "None",
		"sequentialDownload": "true",
		"firstLastPiecePrio": "true",
	}
	for key, value := range want {
		if got.Get(key) != value {
			t.Errorf("add form %s = %q, want %q", key, got.Get(key), value)
		}
	}
}

func TestAddOmitsDisabledStreamingFlags(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse add form: %v", err)
			return
		}
		got = r.PostForm
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	off := false
	c := New(Options{BaseURL: srv.URL, Category: "seedstream", SequentialOrder: &off, FirstLastFirst: &off})
	if err := c.Add(context.Background(), AddOptions{
		URL:        "magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Sequential: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if v := got.Get("sequentialDownload"); v != "" {
		t.Errorf("sequentialDownload sent although disabled: %q", v)
	}
	if v := got.Get("firstLastPiecePrio"); v != "" {
		t.Errorf("firstLastPiecePrio sent although disabled: %q", v)
	}
}

func TestAddSendsOnlyWantedStreamingFlags(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse add form: %v", err)
			return
		}
		got = r.PostForm
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	off := false
	c := New(Options{BaseURL: srv.URL, Category: "seedstream", FirstLastFirst: &off})
	if err := c.Add(context.Background(), AddOptions{
		URL:        "magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Sequential: true,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if v := got.Get("sequentialDownload"); v != "true" {
		t.Errorf("sequentialDownload = %q, want \"true\"", v)
	}
	if v := got.Get("firstLastPiecePrio"); v != "" {
		t.Errorf("firstLastPiecePrio sent although disabled: %q", v)
	}
}
