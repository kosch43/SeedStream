package stremio

import (
	"context"
	"sort"
	"strings"
	"time"

	"seedstream/pkg/core/config"

	"seedstream/pkg/core/persistence"
	"seedstream/pkg/torrent"
)

// CerberusStatus is everything the watchdog knows, in one payload.
//
// All of it previously existed only as log lines, which meant the thing an
// operator most needs to check — whether a tracker obligation is about to be
// breached — required reading server logs. Anything the watchdog can warn about
// should be visible without an SSH session.
type CerberusStatus struct {
	Enabled   bool                         `json:"enabled"`
	Summary   CerberusSummary              `json:"summary"`
	Torrents  []torrent.HnRStatus          `json:"torrents"`
	Blocklist []persistence.BlocklistEntry `json:"blocklist"`
}

// CerberusSummary is the at-a-glance count of what needs attention.
type CerberusSummary struct {
	Tracked  int `json:"tracked"`
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Watch    int `json:"watch"`
	Met      int `json:"met"`
	Unknown  int `json:"unknown"`
	Blocked  int `json:"blocked"`
}

// CerberusStatus reports hit-and-run standing per tracked torrent plus the
// health blocklist. Read-only: it changes nothing.
func (s *Server) CerberusStatus(ctx context.Context) CerberusStatus {
	s.mu.RLock()
	cfg := s.config
	cer := s.cerberusClient
	mgr := s.torrentManager
	s.mu.RUnlock()

	out := CerberusStatus{
		Torrents:  []torrent.HnRStatus{},
		Blocklist: []persistence.BlocklistEntry{},
	}
	if cer == nil {
		return out
	}
	if bl := cer.Blocklist(200); bl != nil {
		out.Blocklist = bl
	}
	out.Summary.Blocked = len(out.Blocklist)

	// Cerberus needs a torrent client to observe anything. Reporting enabled on
	// the registry alone would leave a page of zeros with no explanation of why.
	if mgr == nil || !mgr.Enabled() {
		return out
	}
	out.Enabled = true
	entries, err := mgr.ListAll(ctx)
	if err != nil {
		return out
	}

	now := time.Now()
	for _, e := range entries {
		rec := cer.GetContentByHash(e.Hash)
		if rec == nil {
			continue // not a torrent SeedStream tracks
		}
		st := torrent.EvaluateHnR(e, rec.IndexerName,
			hnrRulesFor(cfg, rec.IndexerName), hnrWindowDaysFor(cfg, rec.IndexerName), now)
		out.Torrents = append(out.Torrents, st)

		switch st.Risk {
		case torrent.HnRRiskCritical:
			out.Summary.Critical++
		case torrent.HnRRiskWarning:
			out.Summary.Warning++
		case torrent.HnRRiskWatch:
			out.Summary.Watch++
		case torrent.HnRRiskMet:
			out.Summary.Met++
		default:
			out.Summary.Unknown++
		}
	}
	out.Summary.Tracked = len(out.Torrents)

	// Most urgent first, so whatever needs action is at the top.
	sort.SliceStable(out.Torrents, func(i, j int) bool {
		return riskOrder(out.Torrents[i].Risk) < riskOrder(out.Torrents[j].Risk)
	})
	return out
}

func riskOrder(r torrent.HnRRisk) int {
	switch r {
	case torrent.HnRRiskCritical:
		return 0
	case torrent.HnRRiskWarning:
		return 1
	case torrent.HnRRiskWatch:
		return 2
	case torrent.HnRRiskOK:
		return 3
	case torrent.HnRRiskMet:
		return 4
	default:
		return 5
	}
}

// hnrRulesFor resolves a tracker's hit-and-run rules from config by name.
func hnrRulesFor(cfg *config.Config, indexerName string) *torrent.HnRRules {
	if cfg == nil || indexerName == "" {
		return nil
	}
	for _, idx := range cfg.Indexers {
		if !strings.EqualFold(idx.Name, indexerName) {
			continue
		}
		if idx.HnRMinSeedHours <= 0 && idx.HnRMinRatio <= 0 {
			return nil
		}
		return &torrent.HnRRules{
			MinSeedHours: idx.HnRMinSeedHours,
			MinRatio:     idx.HnRMinRatio,
			Mode:         idx.HnRMode,
		}
	}
	return nil
}

// hnrWindowDaysFor resolves the tracker's deadline in days, 0 when unknown.
func hnrWindowDaysFor(cfg *config.Config, indexerName string) float64 {
	if cfg == nil || indexerName == "" {
		return 0
	}
	for _, idx := range cfg.Indexers {
		if strings.EqualFold(idx.Name, indexerName) {
			return idx.HnRWindowDays
		}
	}
	return 0
}
