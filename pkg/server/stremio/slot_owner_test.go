package stremio

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"seedstream/pkg/auth"
)

func TestCanonicalizeStreamSlotIDBindsLegacySlotToCurrentStream(t *testing.T) {
	got, err := canonicalizeStreamSlotID("stream:movie:tt123:2", &auth.Stream{Username: "alice"})
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if want := "stream:alice:movie:tt123:2"; got != want {
		t.Fatalf("canonical slot = %q, want %q", got, want)
	}
}

func TestCanonicalizeStreamSlotIDRejectsForeignStream(t *testing.T) {
	_, err := canonicalizeStreamSlotID("stream:bob:movie:tt123:2", &auth.Stream{Username: "alice"})
	if !errors.Is(err, errForeignStreamSlot) {
		t.Fatalf("expected foreign slot error, got %v", err)
	}
}

func TestCanonicalizeFailoverOrderPreservesLegacyAndDropsForeign(t *testing.T) {
	order := canonicalizeFailoverOrder([]string{
		"stream:movie:tt123:0",
		"stream:bob:movie:tt123:1",
	}, &auth.Stream{Username: "alice"})
	if len(order) != 1 || order[0] != "stream:alice:movie:tt123:0" {
		t.Fatalf("canonical order = %#v", order)
	}
}

func TestParseStreamSlotIDRejectsUnknownShape(t *testing.T) {
	if _, _, _, _, ok := parseStreamSlotID("stream:not-a-content-type:tt123:0"); ok {
		t.Fatal("expected malformed slot to be rejected")
	}
}

func TestHandlePlayRejectsForeignQualifiedSlot(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/play/stream:bob:movie:tt123:0", nil)
	rec := httptest.NewRecorder()
	(&Server{}).handlePlay(rec, req, &auth.Stream{Username: "alice"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
