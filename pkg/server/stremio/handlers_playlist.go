package stremio

import (
	"context"
	"strings"
	"time"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/release"
	"seedstream/pkg/search/triage"
)

type namedIndexer interface {
	Name() string
}

func indexerNameFromRelease(rel *release.Release) string {
	if rel == nil {
		return ""
	}
	if name := strings.TrimSpace(rel.Indexer); name != "" {
		return name
	}
	if rel.SourceIndexer != nil {
		if n, ok := rel.SourceIndexer.(namedIndexer); ok {
			if name := strings.TrimSpace(n.Name()); name != "" {
				return name
			}
		}
	}
	return ""
}

type playlistResult struct {
	Candidates []triage.Candidate
	Params     *SearchParams

	// SlotPaths, when set, gives the exact play path for each candidate (e.g. from failover order).
	// Must match len(Candidates); buildStreamsFromPlaylist uses SlotPaths[i] instead of key.SlotPath(i).
	SlotPaths []string
}

type rawSearchResult struct {
	Params          *SearchParams
	IndexerReleases []*release.Release
}

type playlistSource struct {
	Params   *SearchParams
	Releases []*release.Release
}

const playlistCacheTTL = 10 * time.Minute

type playlistCacheEntry struct {
	result *playlistResult
	until  time.Time
}

type rawSearchCacheEntry struct {
	raw   *rawSearchResult
	until time.Time
}

// filterPlaylistByOrder keeps only candidates whose slot path appears in order (same key, valid index), in that order.
// SlotPaths on the result are set from order so stream URLs match the client. Non-slot-path entries are ignored.
func filterPlaylistByOrder(list *playlistResult, key StreamSlotKey, order []string) *playlistResult {
	if list == nil || len(order) == 0 {
		return list
	}
	maxIndex := len(list.Candidates) - 1
	var filtered []triage.Candidate
	var paths []string
	for _, entry := range order {
		if !strings.HasPrefix(entry, streamSlotPrefix) {
			continue
		}
		sid, ct, id, idx, ok := parseStreamSlotID(entry)
		if !ok || idx < 0 || idx > maxIndex {
			continue
		}
		if ct != key.ContentType || id != key.ID {
			continue
		}
		if sid != "" && sid != key.StreamID {
			continue
		}
		filtered = append(filtered, list.Candidates[idx])
		paths = append(paths, entry)
	}
	if len(filtered) == 0 {
		return list
	}
	return &playlistResult{
		Candidates: filtered,
		Params:     list.Params,
		SlotPaths:  paths,
	}
}

// buildPlaylist returns the candidate play list for (stream, type, id).
// Raw search and play list are both cached by the stable stream slot key.
// Relevant config changes clear these caches centrally after successful saves.
func (s *Server) buildPlaylist(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	cacheKey := key.CacheKey()
	if v, ok := s.playlistCache.Load(cacheKey); ok {
		if ent, _ := v.(*playlistCacheEntry); ent != nil && time.Now().Before(ent.until) {
			candidateCount := 0
			if ent.result != nil {
				candidateCount = len(ent.result.Candidates)
			}
			logger.Debug("Playback playlist cache hit", "key", cacheKey, "candidates", candidateCount)
			return ent.result, nil
		}
	}
	logger.Debug("Playback playlist cache miss", "key", cacheKey)
	list, err := s.buildPlaylistUncached(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return list, err
	}
	s.playlistCache.Store(cacheKey, &playlistCacheEntry{result: list, until: time.Now().Add(playlistCacheTTL)})
	return list, nil
}

func (s *Server) buildPlaylistUncached(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	raw, err := s.getOrBuildRawSearchResult(ctx, key.ContentType, key.ID, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	return s.buildPlaylistFromRaw(raw, isAIOStreams, stream)
}

func (s *Server) getOrBuildRawSearchResult(ctx context.Context, contentType, id string, stream *auth.Stream) (*rawSearchResult, error) {
	rawKey := streamID(stream) + ":" + contentType + ":" + id
	if v, ok := s.rawSearchCache.Load(rawKey); ok {
		if ent, _ := v.(*rawSearchCacheEntry); ent != nil && time.Now().Before(ent.until) {
			releaseCount := 0
			if ent.raw != nil {
				releaseCount = len(ent.raw.IndexerReleases)
			}
			logger.Debug("Playback candidate cache hit", "key", rawKey, "releases", releaseCount)
			return cloneRawSearchResult(ent.raw), nil
		}
	}
	logger.Debug("Playback candidate cache miss", "key", rawKey)
	raw, err := s.buildRawSearchResult(ctx, contentType, id, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	s.rawSearchCache.Store(rawKey, &rawSearchCacheEntry{raw: raw, until: time.Now().Add(playlistCacheTTL)})
	return cloneRawSearchResult(raw), nil
}

func (s *Server) GetSearchReleases(ctx context.Context, contentType, id string) (*SearchReleasesResponse, error) {
	fallbackStream := &auth.Stream{Username: defaultStreamID}
	if contentType == "movie" {
		fallbackStream.MovieSearchQueries = allSearchQueryNames(s.config.MovieSearchQueries)
	} else {
		fallbackStream.SeriesSearchQueries = allSearchQueryNames(s.config.SeriesSearchQueries)
	}
	raw, err := s.getOrBuildRawSearchResult(ctx, contentType, id, fallbackStream)
	if err != nil || raw == nil {
		return nil, err
	}
	source := buildPlaylistSource(raw)

	releasesOut := make([]SearchReleaseTag, 0, len(source.Releases))
	for _, r := range source.Releases {
		if r == nil {
			continue
		}
		idxName := r.Indexer
		if idxName == "" && r.SourceIndexer != nil {
			if idx, ok := r.SourceIndexer.(indexer.Indexer); ok {
				idxName = idx.Name()
			}
		}
		if idxName == "" {
			idxName = "Tracker"
		}
		releasesOut = append(releasesOut, SearchReleaseTag{
			Title:      r.Title,
			Link:       r.Link,
			DetailsURL: r.DetailsURL,
			Size:       r.Size,
			Indexer:    idxName,
		})
	}

	return &SearchReleasesResponse{Releases: releasesOut}, nil
}

func buildAllReleasesFromRaw(raw *rawSearchResult) []*release.Release {
	var out []*release.Release
	for _, rel := range raw.IndexerReleases {
		if rel == nil {
			continue
		}
		if release.IsFullDiscRelease(rel.Title) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func buildPlaylistSource(raw *rawSearchResult) *playlistSource {
	if raw == nil {
		return &playlistSource{}
	}
	return &playlistSource{
		Params:   raw.Params,
		Releases: buildAllReleasesFromRaw(raw),
	}
}

func releasesToCandidates(releases []*release.Release) []triage.Candidate {
	var out []triage.Candidate
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		out = append(out, triage.Candidate{Release: rel, Score: 0})
	}
	return out
}

func (s *Server) buildPlaylistFromRaw(raw *rawSearchResult, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	filterMode, filteringActive := resolveFilterMode(stream)
	source := buildPlaylistSource(raw)
	candidates := buildPlaylistCandidates(source)
	candidates = s.applyPlaylistFiltering(candidates, isAIOStreams, filteringActive, filterMode, stream)
	candidates = applyPlaylistSorting(candidates, s.triageService, filteringActive, filterMode, stream)
	return buildPlaylistResult(source, candidates), nil
}

func resolveFilterMode(stream *auth.Stream) (string, bool) {
	filterMode := "none"
	if stream != nil && strings.TrimSpace(stream.FilterSortingMode) != "" {
		filterMode = strings.ToLower(strings.TrimSpace(stream.FilterSortingMode))
	}
	return filterMode, filterMode != "none"
}

func cloneReleaseForPlaylist(rel *release.Release) *release.Release {
	if rel == nil {
		return nil
	}
	next := *rel
	if rel.Languages != nil {
		next.Languages = append([]string(nil), rel.Languages...)
	}
	return &next
}

func cloneRawSearchResult(raw *rawSearchResult) *rawSearchResult {
	if raw == nil {
		return nil
	}
	next := &rawSearchResult{
		Params: cloneSearchParams(raw.Params),
	}
	if raw.IndexerReleases != nil {
		next.IndexerReleases = make([]*release.Release, 0, len(raw.IndexerReleases))
		for _, rel := range raw.IndexerReleases {
			next.IndexerReleases = append(next.IndexerReleases, cloneReleaseForPlaylist(rel))
		}
	}
	return next
}

func playlistSlotPaths(list *playlistResult, key StreamSlotKey) []string {
	if list == nil || len(list.Candidates) == 0 {
		return nil
	}
	if len(list.SlotPaths) == len(list.Candidates) {
		return append([]string(nil), list.SlotPaths...)
	}
	paths := make([]string, len(list.Candidates))
	for i := range list.Candidates {
		paths[i] = key.SlotPath(i)
	}
	return paths
}

// filterCandidates dedups by normalized title for AIOStreams profiles so each
// distinct release appears once regardless of how many trackers carry it.
func filterCandidates(merged []triage.Candidate, isAIOStreams bool) []triage.Candidate {
	if !isAIOStreams {
		return merged
	}
	seenTitle := make(map[string]bool)
	filtered := make([]triage.Candidate, 0, len(merged))
	for _, c := range merged {
		if c.Release == nil {
			continue
		}
		if c.Release.Title != "" {
			titleKey := release.NormalizeTitleForDedup(c.Release.Title)
			if titleKey != "" {
				if seenTitle[titleKey] {
					continue
				}
				seenTitle[titleKey] = true
			}
		}
		filtered = append(filtered, c)
	}
	return filtered
}

func buildPlaylistCandidates(source *playlistSource) []triage.Candidate {
	if source == nil {
		return nil
	}
	return releasesToCandidates(source.Releases)
}

func (s *Server) applyPlaylistFiltering(candidates []triage.Candidate, isAIOStreams, filteringActive bool, filterMode string, stream *auth.Stream) []triage.Candidate {
	inputResults := len(candidates)
	candidates = filterCandidates(candidates, isAIOStreams)
	if filteringActive {
		logStreamFiltering(stream, filterMode, inputResults, len(candidates))
	}
	return candidates
}

func applyPlaylistSorting(candidates []triage.Candidate, triageService *triage.Service, filteringActive bool, filterMode string, stream *auth.Stream) []triage.Candidate {
	if !filteringActive {
		return candidates
	}
	inputResults := len(candidates)
	sortCandidates(triageService, candidates)
	logStreamSorting(stream, filterMode, inputResults, len(candidates))
	return candidates
}

func buildPlaylistResult(source *playlistSource, candidates []triage.Candidate) *playlistResult {
	return &playlistResult{
		Candidates: candidates,
		Params:     source.Params,
	}
}

func logStreamFiltering(stream *auth.Stream, filterMode string, inputResults, finalResults int) {
	logger.Debug("Stream filtering",
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
		"mode", filterMode,
		"input_results", inputResults,
		"final_results", finalResults,
	)
}

func sortCandidates(triageService *triage.Service, candidates []triage.Candidate) {
	triageService.SortCandidates(candidates)
}

func logStreamSorting(stream *auth.Stream, filterMode string, inputResults, finalResults int) {
	logger.Debug("Stream sorting",
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
		"mode", filterMode,
		"input_results", inputResults,
		"final_results", finalResults,
	)
}
