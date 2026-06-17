package persistence

import (
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"strings"
	"time"
)

// normalizeInfoHash converts any BitTorrent info hash representation to
// lowercase hex (40 chars). Magnet URIs may encode the hash as base32
// (32 uppercase A-Z2-7 chars) instead of hex; both forms are accepted and
// normalised to the 40-char hex format that qBittorrent uses internally.
func normalizeInfoHash(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) == 40 {
		return h // already hex
	}
	if len(h) == 32 {
		// BitTorrent base32: 20 bytes encoded as 32 base32 chars (no padding).
		// 32 chars × 5 bits = 160 bits = 20 bytes — exactly one SHA1.
		decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded)
		}
	}
	return h
}

// TorrentEntry records the mapping from an info_hash to its content IDs.
type TorrentEntry struct {
	InfoHash     string
	ImdbID       string
	TmdbID       string
	TvdbID       string
	Season       int
	Episode      int
	Magnet       string
	ReleaseTitle string
	IndexerName  string
	AddedAt      time.Time
}

// RegisterTorrent writes an info_hash → content ID mapping. Safe to call with
// nil receiver (no-op).
func (m *StateManager) RegisterTorrent(infoHash, imdbID, tmdbID, tvdbID string, season, episode int, magnet, releaseTitle, indexerName string) error {
	if m == nil || m.db == nil {
		return nil
	}
	hash := normalizeInfoHash(infoHash)
	if hash == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	return m.withWriteLock(func(db *sql.DB) error {
		_, err := db.Exec(`INSERT OR REPLACE INTO cerberus_torrent_registry
			(info_hash, imdb_id, tmdb_id, tvdb_id, season, episode, magnet, release_title, indexer_name, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hash, imdbID, tmdbID, tvdbID, season, episode, magnet, releaseTitle, indexerName, now)
		return err
	})
}

// ReportTorrentFailure marks an info_hash as bad and increments its failure
// count. Content IDs are copied from the registry if present.
func (m *StateManager) ReportTorrentFailure(infoHash, reason string) error {
	if m == nil || m.db == nil {
		return nil
	}
	hash := normalizeInfoHash(infoHash)
	if hash == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	return m.withWriteLock(func(db *sql.DB) error {
		var imdbID, tmdbID, tvdbID string
		var season, episode int
		_ = db.QueryRow(`SELECT imdb_id, tmdb_id, tvdb_id, season, episode
			FROM cerberus_torrent_registry WHERE info_hash = ?`, hash).
			Scan(&imdbID, &tmdbID, &tvdbID, &season, &episode)

		tx, err := db.Begin()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT OR IGNORE INTO cerberus_blocklist
			(info_hash, imdb_id, tmdb_id, tvdb_id, season, episode, failure_count, last_failure_at, reason)
			VALUES (?, ?, ?, ?, ?, ?, 0, ?, '')`,
			hash, imdbID, tmdbID, tvdbID, season, episode, now)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		_, err = tx.Exec(`UPDATE cerberus_blocklist
			SET failure_count = failure_count + 1, last_failure_at = ?, reason = ?
			WHERE info_hash = ?`, now, reason, hash)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

// IsInfoHashBlocked returns true if the hash appears in the blocklist.
func (m *StateManager) IsInfoHashBlocked(infoHash string) bool {
	if m == nil || m.db == nil {
		return false
	}
	hash := normalizeInfoHash(infoHash)
	if hash == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var count int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM cerberus_blocklist WHERE info_hash = ?`, hash).Scan(&count)
	return count > 0
}

// GetBlockedHashes returns all blocked info_hashes for a given content.
// Returns nil if all ID fields are empty to prevent matching unrelated content.
func (m *StateManager) GetBlockedHashes(imdbID, tmdbID, tvdbID string, season, episode int) []string {
	if m == nil || m.db == nil {
		return nil
	}
	if imdbID == "" && tmdbID == "" && tvdbID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows, err := m.db.Query(`SELECT info_hash FROM cerberus_blocklist
		WHERE imdb_id = ? AND tmdb_id = ? AND tvdb_id = ? AND season = ? AND episode = ?`,
		imdbID, tmdbID, tvdbID, season, episode)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err == nil {
			hashes = append(hashes, h)
		}
	}
	return hashes
}

// GetTorrentByHash looks up the registry entry for the given info_hash.
// Returns nil if not found.
func (m *StateManager) GetTorrentByHash(infoHash string) *TorrentEntry {
	if m == nil || m.db == nil {
		return nil
	}
	hash := normalizeInfoHash(infoHash)
	if hash == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var e TorrentEntry
	var addedAtMs int64
	err := m.db.QueryRow(`SELECT info_hash, imdb_id, tmdb_id, tvdb_id, season, episode,
		magnet, release_title, indexer_name, added_at
		FROM cerberus_torrent_registry WHERE info_hash = ?`, hash).
		Scan(&e.InfoHash, &e.ImdbID, &e.TmdbID, &e.TvdbID, &e.Season, &e.Episode,
			&e.Magnet, &e.ReleaseTitle, &e.IndexerName, &addedAtMs)
	if err != nil {
		return nil
	}
	e.AddedAt = time.UnixMilli(addedAtMs)
	return &e
}
