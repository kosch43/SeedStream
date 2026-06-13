package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"seedstream/pkg/core/config"
)

type memoryAvailNZBStore struct {
	mu   sync.Mutex
	data map[string]json.RawMessage
}

func newMemoryAvailNZBStore() *memoryAvailNZBStore {
	return &memoryAvailNZBStore{data: make(map[string]json.RawMessage)}
}

func (s *memoryAvailNZBStore) Get(key string, target interface{}) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, ok := s.data[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return false, err
	}
	return true, nil
}

func (s *memoryAvailNZBStore) Set(key string, value interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.data[key] = raw
	return nil
}

func TestEnsureAvailNZBReadyForReloadUsesStoredKey(t *testing.T) {
	store := newMemoryAvailNZBStore()
	if err := store.Set("availnzb_api_key", map[string]string{"token": "stored-key"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/me" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "stored-key" {
			t.Fatalf("X-API-Key = %q, want %q", got, "stored-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app-1","name":"SeedStream","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":null,"last_rollback_at":null}`))
	}))
	defer ts.Close()

	srv := &Server{
		availNZBURL:   ts.URL,
		availNZBStore: store,
	}

	srv.ensureAvailNZBReadyForReload(&config.Config{AvailNZBMode: "on"})

	srv.mu.RLock()
	got := srv.availNZBAPIKey
	srv.mu.RUnlock()

	if got != "stored-key" {
		t.Fatalf("availNZBAPIKey = %q, want %q", got, "stored-key")
	}
}

func TestEnsureAvailNZBReadyForReloadRegistersMissingKey(t *testing.T) {
	store := newMemoryAvailNZBStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys/recover" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"recover failed"}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/keys/roll_key" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":    "1",
			"name":  "SeedStream",
			"token": "registered-key",
		})
	}))
	defer ts.Close()

	srv := &Server{
		availNZBURL:   ts.URL,
		availNZBStore: store,
	}

	srv.ensureAvailNZBReadyForReload(&config.Config{AvailNZBMode: "on"})

	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.mu.RLock()
		got := srv.availNZBAPIKey
		srv.mu.RUnlock()
		if got == "registered-key" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for key registration, key=%q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var stored struct {
		Token string `json:"token"`
	}
	found, err := store.Get("availnzb_api_key", &stored)
	if err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	if !found {
		t.Fatal("expected stored API key after registration")
	}
	if stored.Token != "registered-key" {
		t.Fatalf("stored token = %q, want %q", stored.Token, "registered-key")
	}
}
