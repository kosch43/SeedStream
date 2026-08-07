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
	// Type of client: "qbittorrent" or "transmission". Empty means qBittorrent,
	// so configurations written before Transmission was supported keep working.
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

// Supported download client kinds.
const (
	TorrentClientQbittorrent  = "qbittorrent"
	TorrentClientTransmission = "transmission"
)

// SupportedTorrentClientTypes lists the kinds SeedStream can drive, for the UI.
func SupportedTorrentClientTypes() []string {
	return []string{TorrentClientQbittorrent, TorrentClientTransmission}
}

// NormalizeTorrentClientType canonicalises a configured client type.
//
// An empty type means qBittorrent: every configuration written before
// Transmission was supported omits the field, and they must keep working
// without being rewritten. An unrecognised type is returned lowercased so the
// caller can reject it by name rather than silently treating it as qBittorrent
// and pointing a Transmission URL at a qBittorrent client.
func NormalizeTorrentClientType(t string) string {
	switch s := strings.ToLower(strings.TrimSpace(t)); s {
	case "", TorrentClientQbittorrent, "qbit", "qb":
		return TorrentClientQbittorrent
	case TorrentClientTransmission, "transmission-daemon", "transmissionbt":
		return TorrentClientTransmission
	default:
		return s
	}
}

// IsTorrentIndexerType reports whether an indexer type denotes a torrent (Torznab)
// source rather than a Usenet (Newznab) source.
func IsTorrentIndexerType(indexerType string) bool {
	switch strings.ToLower(strings.TrimSpace(indexerType)) {
	case "torznab", "torrent", "cardigann":
		return true
	default:
		return false
	}
}

// IsDefinitionIndexerType reports whether an indexer is driven by a bundled
// tracker definition (scraping the tracker's own site) rather than by a Torznab
// endpoint.
func IsDefinitionIndexerType(indexerType string) bool {
	return strings.EqualFold(strings.TrimSpace(indexerType), "cardigann")
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
