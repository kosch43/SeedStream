package config

import "strings"

// TorrentClientConfig describes a download client that receives torrents picked
// for playback and keeps them seeding (ratio-safe for private trackers).
//
// SeedStream itself does not seed — it hands the torrent to a real client
// (qBittorrent) running on a seedbox, which downloads sequentially for instant
// playback and then continues seeding indefinitely. This is what keeps ratio
// healthy on private trackers; nothing is leeched-and-discarded.
type TorrentClientConfig struct {
	Name string `json:"name"`
	// Type of client. Currently "qbittorrent" is supported.
	Type string `json:"type"`
	// URL of the client's WebUI, e.g. http://seedbox:8080 (no trailing slash needed).
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	// Category applied to torrents SeedStream adds, so they are easy to identify
	// and manage on the seedbox. Default "seedstream".
	Category string `json:"category,omitempty"`
	// SavePath is the local path SeedStream reads downloads from. On a
	// same-machine setup this is identical to qBittorrent's own save path.
	// When qBittorrent runs on a remote seedbox, set this to the local mount
	// point (e.g. /mnt/seedbox) and set RemotePath to qBittorrent's path.
	SavePath string `json:"save_path,omitempty"`
	// RemotePath is qBittorrent's save path on its own machine (e.g.
	// /downloads/seedstream). Only needed when qBittorrent is on a different
	// host than SeedStream. SeedStream strips this prefix from paths reported
	// by the qBittorrent API and replaces it with SavePath, so the file is
	// read from the correct local mount point.
	// Leave empty when both services share a filesystem.
	RemotePath string `json:"remote_path,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

// IsTorrentIndexerType reports whether an indexer type denotes a torrent (Torznab)
// source rather than a Usenet (Newznab) source.
func IsTorrentIndexerType(indexerType string) bool {
	switch strings.ToLower(strings.TrimSpace(indexerType)) {
	case "torznab", "torrent":
		return true
	default:
		return false
	}
}

func (t TorrentClientConfig) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

func (t TorrentClientConfig) CategoryOrDefault() string {
	if c := strings.TrimSpace(t.Category); c != "" {
		return c
	}
	return "seedstream"
}
