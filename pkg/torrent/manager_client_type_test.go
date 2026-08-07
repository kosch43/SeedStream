package torrent

import (
	"testing"

	"seedstream/pkg/core/config"
	"seedstream/pkg/torrent/qbittorrent"
	"seedstream/pkg/torrent/transmission"
)

// TestClientTypeSelection: the configured type decides which client is built.
// Getting this wrong would point a Transmission URL at a qBittorrent client,
// which fails in a way that looks like bad credentials rather than a wrong kind.
func TestClientTypeSelection(t *testing.T) {
	cases := []struct {
		name    string
		cfgType string
		want    string // "qbit", "transmission", or "none"
	}{
		// Empty means qBittorrent: every config written before Transmission
		// existed omits the field and must keep working untouched.
		{"empty defaults to qbittorrent", "", "qbit"},
		{"explicit qbittorrent", "qbittorrent", "qbit"},
		{"case insensitive", "qBittorrent", "qbit"},
		{"transmission", "transmission", "transmission"},
		{"transmission daemon alias", "transmission-daemon", "transmission"},
		// An unknown kind is skipped rather than silently treated as qBittorrent.
		{"unknown kind is skipped", "deluge", "none"},
	}
	for _, tc := range cases {
		mgr := NewManager([]config.TorrentClientConfig{{
			Name: "box", Type: tc.cfgType, URL: "http://host:1234", Category: "seedstream",
		}})
		switch tc.want {
		case "none":
			if mgr.Enabled() {
				t.Errorf("%s: an unsupported client must not be used", tc.name)
			}
			continue
		}
		if !mgr.Enabled() || len(mgr.clients) != 1 {
			t.Errorf("%s: expected one client", tc.name)
			continue
		}
		switch tc.want {
		case "qbit":
			if _, ok := mgr.clients[0].client.(*qbittorrent.Client); !ok {
				t.Errorf("%s: expected a qBittorrent client, got %T", tc.name, mgr.clients[0].client)
			}
		case "transmission":
			if _, ok := mgr.clients[0].client.(*transmission.Client); !ok {
				t.Errorf("%s: expected a Transmission client, got %T", tc.name, mgr.clients[0].client)
			}
		}
	}
}

// TestClientWithoutURLIsSkipped: a half-configured entry must not become a
// client that fails on every call.
func TestClientWithoutURLIsSkipped(t *testing.T) {
	mgr := NewManager([]config.TorrentClientConfig{{Name: "box", Type: "transmission"}})
	if mgr.Enabled() {
		t.Fatal("an entry with no URL is not a usable client")
	}
}
