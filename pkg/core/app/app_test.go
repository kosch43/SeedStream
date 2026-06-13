package app

import (
	"testing"

	"seedstream/pkg/services/availnzb"
)

func TestSetAvailNZBAPIKeyUpdatesOptsAndLiveClient(t *testing.T) {
	t.Parallel()

	client := availnzb.NewClient("https://snzb.stream", "")
	a := &App{
		components: &Components{AvailClient: client},
	}

	a.SetAvailNZBAPIKey(" updated-key ")

	if got := client.GetAPIKey(); got != "updated-key" {
		t.Fatalf("client key = %q, want %q", got, "updated-key")
	}
	if got := a.opts.AvailNZBAPIKey; got != "updated-key" {
		t.Fatalf("stored opts key = %q, want %q", got, "updated-key")
	}
}
