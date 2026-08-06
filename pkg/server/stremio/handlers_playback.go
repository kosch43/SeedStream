package stremio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

const defaultStreamID = "default"

const streamSlotPrefix = "stream:"

// maxPlayFallbackAttempts bounds how many alternative slots a single /play
// request will try after the requested torrent fails to prepare. Keeps the
// HTTP request from stalling through the whole candidate list.
const maxPlayFallbackAttempts = 3

// minPrepareBudget is the least time worth handing to another fallback attempt.
// Below this a further attempt cannot realistically add or buffer a torrent, so
// the request fails with a useful error instead of stalling until the client or
// an upstream proxy times out.
const minPrepareBudget = 5 * time.Second

func (s *Server) buildStreamsForKey(ctx context.Context, key StreamSlotKey, stream *auth.Stream, baseURL string) ([]Stream, *playlistResult, error) {
	list, err := s.bootstrapPlaylistForPlay(ctx, key, stream)
	if err != nil {
		if strings.Contains(err.Error(), "no candidates found") {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	streamName := "SeedStream"
	showAll := streamResultsMode(stream) == "display_all"
	return buildStreamsFromPlaylist(list, key, streamName, baseURL, showAll), list, nil
}

// bootstrapPlaylistForPlay rebuilds the play list and deferred sessions the same way as /stream.
func (s *Server) bootstrapPlaylistForPlay(ctx context.Context, key StreamSlotKey, stream *auth.Stream) (*playlistResult, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil {
		return nil, err
	}
	if list == nil || len(list.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates found")
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil || len(list.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates found")
	}
	for _, slotPath := range list.SlotPaths {
		s.sessionManager.ClearSlotFailedDuringPlayback(slotPath)
	}
	s.ensureDeferredSessionsForPlaylist(list, key, stream)
	return list, nil
}

// recoverPlaySessionAfterEviction re-runs the /stream bootstrap and resolves to a playable slot.
// Prioritizes the requested slot index from sessionID, falling back sequentially and wrapping around.
func (s *Server) recoverPlaySessionAfterEviction(ctx context.Context, sessionID string, stream *auth.Stream) (*session.Session, string, error) {
	streamId, contentType, id, requestedIndex, ok := parseStreamSlotID(sessionID)
	if !ok {
		return nil, "", fmt.Errorf("invalid slot path %q", sessionID)
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
	list, err := s.bootstrapPlaylistForPlay(ctx, key, stream)
	if err != nil {
		return nil, "", err
	}
	currentID := ""
	currentIndex := -1
	startAtRequested := false
	if requestedIndex < len(list.Candidates) {
		currentIndex = requestedIndex - 1
		startAtRequested = true
	}
	scannedFromStart := !startAtRequested

	for {
		nextID := s.deriveNextSlotIDFromPlaylist(currentID, key, currentIndex, list, stream)
		if nextID == "" {
			if !scannedFromStart {
				scannedFromStart = true
				currentID = ""
				currentIndex = -1
				continue
			}
			return nil, "", fmt.Errorf("no playable slot in rebuilt play list")
		}
		nextSess, err := s.sessionManager.GetSession(nextID)
		if err != nil {
			_, _, _, nextIndex, nextOK := parseStreamSlotID(nextID)
			if !nextOK {
				return nil, "", fmt.Errorf("invalid recovered slot %q", nextID)
			}
			nextSess, err = s.resolveStreamSlotFromPlaylist(key, nextIndex, list, stream)
		}
		if err == nil {
			return nextSess, nextID, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		logger.Debug("Play recovery skipped unresolvable slot", "slot", nextID, "err", err)
		s.sessionManager.SetSlotFailedDuringPlayback(nextID)
		currentID = nextID
		_, _, _, currentIndex, _ = parseStreamSlotID(currentID)
	}
}

func (s *Server) applyExposedPlaylistOrder(list *playlistResult, key StreamSlotKey, stream *auth.Stream) *playlistResult {
	if list == nil {
		return nil
	}
	if !streamUsesAIOStreamsProfile(stream) {
		return list
	}
	if order := s.sessionManager.GetStreamFailoverOrder(streamToken(stream), key.CacheKey()); len(order) > 0 {
		list = filterPlaylistByOrder(list, key, order)
	}
	return list
}

func (s *Server) GetStreams(ctx context.Context, contentType, id string, stream *auth.Stream) ([]Stream, error) {
	const streamRequestTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, streamRequestTimeout)
	defer cancel()
	baseURL := s.baseURLWithToken(stream)
	key := StreamSlotKey{StreamID: streamID(stream), ContentType: contentType, ID: id}
	streams, _, err := s.buildStreamsForKey(ctx, key, stream, baseURL)
	if err != nil {
		return nil, err
	}
	if sink := getStreamSinkFromContext(ctx); sink != nil {
		for _, st := range streams {
			if !sink(st) {
				break
			}
		}
	}
	return streams, nil
}

func forceDisconnect(w http.ResponseWriter, r *http.Request, baseURL string) {
	errorVideoURL := strings.TrimSuffix(baseURL, "/") + "/error/failure.mp4"
	logger.Info("Redirecting to error video", "url", errorVideoURL)

	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, r, errorVideoURL, http.StatusTemporaryRedirect)
}

type StreamSlotKey struct {
	StreamID    string
	ContentType string
	ID          string
}

func (k StreamSlotKey) SlotPath(index int) string {
	return formatStreamSlotPath(k.StreamID, k.ContentType, k.ID, index)
}

func (k StreamSlotKey) CacheKey() string {
	return k.StreamID + ":" + k.ContentType + ":" + k.ID
}

func (k StreamSlotKey) RawCacheKey() string {
	return k.ContentType + ":" + k.ID
}

func (s *Server) baseURLWithToken(stream *auth.Stream) string {
	base := strings.TrimSuffix(s.baseURL, "/")
	if stream != nil && stream.Token != "" {
		base += "/" + stream.Token
	}
	return base
}

func streamToken(stream *auth.Stream) string {
	if stream != nil {
		return stream.Token
	}
	return ""
}

func streamID(stream *auth.Stream) string {
	if stream != nil && strings.TrimSpace(stream.Username) != "" {
		return strings.TrimSpace(stream.Username)
	}
	return defaultStreamID
}

func streamSearchQueryNames(stream *auth.Stream, contentType string) []string {
	if stream == nil {
		return nil
	}
	if contentType == "movie" {
		return append([]string(nil), stream.MovieSearchQueries...)
	}
	return append([]string(nil), stream.SeriesSearchQueries...)
}

func allSearchQueryNames(queries []config.SearchQueryConfig) []string {
	names := make([]string, 0, len(queries))
	for _, query := range queries {
		name := strings.TrimSpace(query.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

func streamUsesAIOStreamsProfile(stream *auth.Stream) bool {
	if stream == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(stream.FilterSortingMode), "aiostreams")
}

func streamIndexerMode(stream *auth.Stream) string {
	if stream == nil || strings.TrimSpace(stream.IndexerMode) == "" {
		return "combine"
	}
	mode := strings.ToLower(strings.TrimSpace(stream.IndexerMode))
	switch mode {
	case "failover":
		return "failover"
	default:
		return "combine"
	}
}

func streamCombinesResults(stream *auth.Stream) bool {
	if stream == nil || stream.CombineResults == nil {
		return true
	}
	return *stream.CombineResults
}

func streamFailoverEnabled(stream *auth.Stream) bool {
	if stream == nil || stream.EnableFailover == nil {
		return true
	}
	return *stream.EnableFailover
}

func streamIndexerSelections(stream *auth.Stream) []string {
	if stream == nil {
		return nil
	}
	return append([]string(nil), stream.IndexerSelections...)
}

func streamIndexerOverrides(stream *auth.Stream) map[string]config.IndexerSearchConfig {
	if stream == nil || len(stream.IndexerOverrides) == 0 {
		return nil
	}
	out := make(map[string]config.IndexerSearchConfig, len(stream.IndexerOverrides))
	for name, override := range stream.IndexerOverrides {
		out[name] = override
	}
	return out
}

func streamResultsMode(stream *auth.Stream) string {
	if streamUsesAIOStreamsProfile(stream) {
		return "display_all"
	}
	if stream == nil || strings.TrimSpace(stream.ResultsMode) == "" {
		return "combined_stream"
	}
	mode := strings.ToLower(strings.TrimSpace(stream.ResultsMode))
	switch mode {
	case "display_all":
		return "display_all"
	default:
		return "combined_stream"
	}
}

func stableIndexerOverridesKey(overrides map[string]config.IndexerSearchConfig) string {
	if len(overrides) == 0 {
		return "none"
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		return "error"
	}
	return string(data)
}

func streamSearchQueryCacheKey(stream *auth.Stream, contentType string) string {
	names := streamSearchQueryNames(stream, contentType)
	queryComponent := "none"
	if streamCombinesResults(stream) && len(names) > 0 {
		sort.Strings(names)
	}
	if len(names) > 0 {
		queryComponent = strings.Join(names, ",")
	}
	return fmt.Sprintf(
		"stream=%s|queries=%s|selected_trackers=%s|overrides=%s|mode=%s|combine=%t|failover=%t|results=%s",
		streamID(stream),
		queryComponent,
		strings.Join(streamIndexerSelections(stream), ","),
		stableIndexerOverridesKey(streamIndexerOverrides(stream)),
		streamIndexerMode(stream),
		streamCombinesResults(stream),
		streamFailoverEnabled(stream),
		streamResultsMode(stream),
	)
}

func hasResolvedIdentifiers(req indexer.SearchRequest) bool {
	return strings.TrimSpace(req.IMDbID) != "" || strings.TrimSpace(req.TMDBID) != "" || strings.TrimSpace(req.TVDBID) != ""
}

func hasUsableIDSearchIdentifier(req indexer.SearchRequest, contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "movie":
		return strings.TrimSpace(req.IMDbID) != "" || strings.TrimSpace(req.TMDBID) != ""
	case "series":
		return strings.TrimSpace(req.TVDBID) != "" || strings.TrimSpace(req.TMDBID) != "" || strings.TrimSpace(req.IMDbID) != ""
	default:
		return hasResolvedIdentifiers(req)
	}
}

func hasPreparedTextQueries(req indexer.SearchRequest) bool {
	return strings.TrimSpace(req.Query) != ""
}

func looksLikeTMDBID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

func applyStreamIndexerSelection(req *indexer.SearchRequest, stream *auth.Stream) {
	if req == nil {
		return
	}
	req.IndexerMode = streamIndexerMode(stream)
	if stream == nil || len(stream.IndexerOverrides) == 0 || len(req.EffectiveByIndexer) == 0 {
		return
	}

	selected := make(map[string]config.IndexerSearchConfig, len(stream.IndexerOverrides))
	for name, override := range stream.IndexerOverrides {
		selected[name] = override
	}

	disableID := true
	disableText := true

	for name, effective := range req.EffectiveByIndexer {
		override, isSelected := selected[name]
		if isSelected {
			if effective == nil {
				copyOverride := override
				req.EffectiveByIndexer[name] = &copyOverride
				continue
			}
			if override.DisableIdSearch != nil {
				effective.DisableIdSearch = override.DisableIdSearch
			}
			if override.DisableStringSearch != nil {
				effective.DisableStringSearch = override.DisableStringSearch
			}
			continue
		}
		if effective == nil {
			req.EffectiveByIndexer[name] = &config.IndexerSearchConfig{
				DisableIdSearch:     &disableID,
				DisableStringSearch: &disableText,
			}
			continue
		}
		effective.DisableIdSearch = &disableID
		effective.DisableStringSearch = &disableText
	}
}

func formatStreamSlotPath(streamID, contentType, id string, index int) string {
	return streamSlotPrefix + streamID + ":" + contentType + ":" + id + ":" + strconv.Itoa(index)
}

type StreamSink func(Stream) bool

type streamSinkKeyType struct{}

var streamSinkKey = streamSinkKeyType{}

func WithStreamSink(ctx context.Context, sink StreamSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, streamSinkKey, sink)
}

func getStreamSinkFromContext(ctx context.Context) StreamSink {
	if ctx == nil {
		return nil
	}
	if sink, ok := ctx.Value(streamSinkKey).(StreamSink); ok {
		return sink
	}
	return nil
}

func (s *Server) ensureDeferredSessionsForPlaylist(list *playlistResult, key StreamSlotKey, stream *auth.Stream) {
	if list == nil || list.Params == nil {
		return
	}
	n := len(list.Candidates)
	createdCount := 0
	reusedCount := 0
	replacedCount := 0
	for i := 0; i < n; i++ {
		cand := list.Candidates[i]
		if cand.Release == nil || (cand.Release.Link == "" && cand.Release.Magnet == "" && cand.Release.InfoHash == "") {
			continue
		}
		playPath := key.SlotPath(i)
		if len(list.SlotPaths) == n {
			playPath = list.SlotPaths[i]
		}
		_, outcome, err := s.sessionManager.CreateDeferredSession(playPath, cand.Release, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream))
		if err != nil {
			logger.Debug("Create deferred session for play list failed", "slot", playPath, "err", err)
			continue
		}
		switch outcome {
		case session.DeferredSessionCreateCreated:
			createdCount++
		case session.DeferredSessionCreateExisting:
			reusedCount++
		case session.DeferredSessionCreateReplaced:
			replacedCount++
		}
	}
	if createdCount > 0 || replacedCount > 0 || reusedCount > 0 {
		logger.Debug(
			"Deferred sessions refreshed",
			"stream", key.StreamID,
			"type", key.ContentType,
			"id", key.ID,
			"candidates", n,
			"created", createdCount,
			"reused", reusedCount,
			"replaced", replacedCount,
		)
	}
}

func (s *Server) resolveStreamSlot(ctx context.Context, key StreamSlotKey, index int, stream *auth.Stream) (*session.Session, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil {
		return nil, fmt.Errorf("build play list: %w", err)
	}
	if list == nil {
		return nil, fmt.Errorf("build play list: no candidates found")
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil || len(list.Candidates) == 0 {
		return nil, fmt.Errorf("build play list: no candidates found")
	}
	return s.resolveStreamSlotFromPlaylist(key, index, list, stream)
}

func (s *Server) resolveStreamSlotFromPlaylist(key StreamSlotKey, index int, list *playlistResult, stream *auth.Stream) (*session.Session, error) {
	requestedSlotPath := key.SlotPath(index)
	candidateIndex := index
	if len(list.SlotPaths) == len(list.Candidates) {
		candidateIndex = -1
		for i, slotPath := range list.SlotPaths {
			if slotPath == requestedSlotPath {
				candidateIndex = i
				break
			}
		}
	}
	if candidateIndex < 0 || candidateIndex >= len(list.Candidates) {
		return nil, fmt.Errorf("slot %s not found in play list", requestedSlotPath)
	}
	cand := list.Candidates[candidateIndex]
	rel := cand.Release
	if rel == nil || (rel.Link == "" && rel.Magnet == "" && rel.InfoHash == "") {
		return nil, fmt.Errorf("no release at slot %s", requestedSlotPath)
	}
	sessionID := requestedSlotPath
	_, _, err := s.sessionManager.CreateDeferredSession(sessionID, rel, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream))
	if err != nil {
		return nil, fmt.Errorf("create deferred session: %w", err)
	}
	sess, err := s.sessionManager.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// handleNextRelease redirects the client to the next non-failed slot.
// For slot :0 (the "next release" stream URL, which is always anchored to slot 0), a per-device cursor
// advances through releases so repeated clicks return :1, :2, :3, ... rather than always :1.
// For slot :N (direct progression from a known position), deriveNextSlotID is used as-is.
func (s *Server) handleNextRelease(w http.ResponseWriter, r *http.Request, stream *auth.Stream) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/next/")
	if sessionID == "" {
		http.Error(w, "Missing stream slot", http.StatusBadRequest)
		return
	}
	streamId, contentType, id, currentIndex, ok := parseStreamSlotID(sessionID)
	if !ok {
		http.Error(w, "Invalid stream slot", http.StatusBadRequest)
		return
	}
	if streamId == "" {
		streamId = defaultStreamID
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}

	var nextSlotID string
	var err error
	if currentIndex == 0 {
		// The "next release" stream URL is always anchored to slot :0 regardless of how many times the
		// user has already clicked "next". Use a cursor so successive clicks advance through the list.
		nextSlotID, err = s.advanceNextReleaseCursor(r.Context(), key, stream)
	} else {
		// Called from a specific non-zero slot (e.g. AIOStreams failover order progression).
		nextSlotID, err = s.deriveNextSlotID(r.Context(), sessionID, stream)
	}
	if err != nil || nextSlotID == "" {
		http.Error(w, "No next release available", http.StatusNotFound)
		return
	}
	nextURL := s.baseURLWithToken(stream) + "/play/" + nextSlotID + "?next=1"
	logger.Info("Next release redirect", "from", sessionID, "to", nextSlotID)
	w.Header().Set("Location", nextURL)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, r, nextURL, http.StatusTemporaryRedirect)
}

type nextReleaseCursor struct {
	mu          sync.Mutex
	pendingSlot string // slot we last redirected to; held idempotent until it commits or fails
	nextIndex   int    // next index to search from when advancing; starts at 1
}

// advanceNextReleaseCursor returns the next non-failed slot for the given (device, key).
// It is commit-gated: after redirecting to a slot, all subsequent calls return the same slot
// until it either commits (was successfully served, tracked by recordedSuccessSessionIDs) or
// fails (marked by SetSlotFailedDuringPlayback). This prevents Stremio's automatic re-requests
// of the /next/ URL from prematurely advancing through the playlist.
func (s *Server) advanceNextReleaseCursor(ctx context.Context, key StreamSlotKey, stream *auth.Stream) (string, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return "", err
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil {
		return "", nil
	}
	n := len(list.Candidates)
	useSlotPaths := len(list.SlotPaths) == n

	stateKey := streamToken(stream) + "|" + key.CacheKey()
	v, _ := s.nextReleaseIndex.LoadOrStore(stateKey, &nextReleaseCursor{nextIndex: 1})
	cursor := v.(*nextReleaseCursor)

	cursor.mu.Lock()
	defer cursor.mu.Unlock()

	// If we have a pending slot, decide whether to stay or advance.
	if cursor.pendingSlot != "" {
		if s.sessionManager.GetSlotFailedDuringPlayback(cursor.pendingSlot) {
			// Pending slot failed; fall through to find the next.
			cursor.pendingSlot = ""
		} else if _, committed := s.recordedSuccessSessionIDs.Load(cursor.pendingSlot); !committed {
			// Pending slot is alive but not yet committed (still loading/probing).
			// This is a Stremio automatic retry of the /next/ URL – return the same slot
			// so we don't prematurely skip to the next release.
			return cursor.pendingSlot, nil
		} else {
			// Committed successfully; the user is intentionally advancing. Fall through.
			cursor.pendingSlot = ""
		}
	}

	// Find the next non-failed slot starting from nextIndex.
	startIdx := cursor.nextIndex
	if startIdx < 1 {
		startIdx = 1
	}
	for i := startIdx; i < n; i++ {
		slotPath := key.SlotPath(i)
		if useSlotPaths {
			slotPath = list.SlotPaths[i]
		}
		if !s.sessionManager.GetSlotFailedDuringPlayback(slotPath) {
			cursor.pendingSlot = slotPath
			cursor.nextIndex = i + 1
			return slotPath, nil
		}
	}
	// All candidates exhausted. Reset so the next call starts fresh (e.g. after
	// failed states are cleared or the user circles back to try again).
	cursor.nextIndex = 1
	cursor.pendingSlot = ""
	return "", nil
}

func parseStreamSlotID(sessionID string) (streamId, contentType, id string, index int, ok bool) {
	if !strings.HasPrefix(sessionID, streamSlotPrefix) {
		return "", "", "", 0, false
	}
	rest := strings.TrimPrefix(sessionID, streamSlotPrefix)
	parts := strings.Split(rest, ":")
	if len(parts) < 3 {
		return "", "", "", 0, false
	}
	index, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return "", "", "", 0, false
	}
	if len(parts) == 3 {
		contentType = parts[0]
		id = parts[1]
		return "", contentType, id, index, true
	}
	streamId = parts[0]
	contentType = parts[1]
	id = strings.Join(parts[2:len(parts)-1], ":")
	return streamId, contentType, id, index, true
}

// handlePlay resolves the play slot's session (recovering after eviction) and
// serves the torrent stream. If the torrent fails to prepare, the slot is
// marked failed and the next fallback slot is tried before giving up.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/play/")
	logger.Debug("Play request", "session", sessionID)

	sess, err := s.sessionManager.GetSession(sessionID)
	if err != nil {
		// The session may have been evicted. If the slot was marked as failed,
		// redirect the client to the next working slot rather than 404.
		if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
			if nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig); nextID != "" && deriveErr == nil {
				nextURL := s.baseURLWithToken(streamConfig) + "/play/" + nextID
				logger.Info("Session deleted (slot failed during playback), redirecting to next", "from", sessionID, "to", nextID)
				w.Header().Set("Location", nextURL)
				w.WriteHeader(http.StatusFound)
				return
			}
			forceDisconnect(w, r, s.baseURL)
			return
		}
		recoveredSess, recoveredID, recoverErr := s.recoverPlaySessionAfterEviction(r.Context(), sessionID, streamConfig)
		if recoverErr != nil {
			logger.Debug("Play: session not found", "slot", sessionID, "err", err, "recovery_err", recoverErr)
			http.Error(w, "Session expired or not found", http.StatusNotFound)
			return
		}
		logger.Info("Play: recovered after cache/session eviction", "requested", sessionID, "playing", recoveredID)
		sess = recoveredSess
		sessionID = recoveredID
	} else if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
		if nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig); nextID != "" && deriveErr == nil {
			nextURL := s.baseURLWithToken(streamConfig) + "/play/" + nextID
			logger.Info("Redirecting to next fallback (slot failed during playback)", "from", sessionID, "to", nextID)
			w.Header().Set("Location", nextURL)
			w.WriteHeader(http.StatusFound)
			return
		}
		forceDisconnect(w, r, s.baseURL)
		return
	}

	// Bound the whole /play request rather than each attempt. Each prepare can
	// wait the full torrent prepare timeout, so N sequential fallbacks used to
	// cost N * timeout — which overruns the ~100s ceiling of common reverse
	// proxies (Cloudflare answers 504/524) long before failover finishes, so the
	// viewer sees a proxy error instead of the next slot. The budget is shared:
	// every attempt gets whatever time is left.
	// Scaled to the head this release needs: a 4K remux asks for several hundred
	// megabytes, which no fixed ninety seconds can deliver, and the expiry used
	// to trigger a failover that started a second copy of the same film.
	playBudget := s.prepareBudget(sess, s.playbackBufferBytes(sess, s.config.TorrentBufferBytes))
	playDeadline := time.Now().Add(playBudget)

	var lastErr error
	for attempt := 0; ; attempt++ {
		remaining := time.Until(playDeadline)
		if remaining < minPrepareBudget {
			logger.Warn("Play: prepare budget exhausted, not trying further fallbacks",
				"session", sessionID, "attempts", attempt)
			if lastErr == nil {
				lastErr = fmt.Errorf("no slot became playable within %s", playBudget)
			}
			break
		}
		s.sessionManager.BeginPlaybackStartup(sessionID)
		lastErr = s.serveTorrent(w, r, sess, streamConfig, remaining)
		s.sessionManager.EndPlaybackStartup(sessionID)
		if lastErr == nil {
			return
		}
		if errors.Is(lastErr, context.Canceled) {
			logger.Debug("Play canceled by client", "session", sessionID)
			return
		}

		// A torrent that is downloading and merely has not finished buffering is
		// not a failed candidate. Marking the slot failed would send the next
		// attempt to a different release and start a second copy of the same
		// film — two 59 GB remuxes competing for the same bandwidth, so the one
		// the viewer is waiting for gets slower. Return and let the player retry
		// this slot, which resumes a download already part-finished.
		if errors.Is(lastErr, torrent.ErrStillBuffering) {
			logger.Info("Play: still buffering, keeping this release rather than starting another",
				"session", sessionID, "attempt", attempt+1, "reason", lastErr)
			break
		}

		// Preparation failed before any response bytes were written: mark the
		// slot failed and try the next fallback slot within the same request.
		logger.Warn("Torrent playback failed, trying next fallback",
			"session", sessionID, "attempt", attempt+1, "err", lastErr)
		s.sessionManager.SetSlotFailedDuringPlayback(sessionID)

		if !streamFailoverEnabled(streamConfig) || attempt+1 >= maxPlayFallbackAttempts {
			break
		}
		nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig)
		if deriveErr != nil || nextID == "" {
			break
		}
		nextSess, nextErr := s.sessionManager.GetSession(nextID)
		if nextErr != nil {
			if streamId, contentType, id, index, ok := parseStreamSlotID(nextID); ok {
				key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
				nextSess, nextErr = s.resolveStreamSlot(r.Context(), key, index, streamConfig)
			}
		}
		if nextErr != nil || nextSess == nil {
			break
		}
		logger.Info("Play: switching to fallback slot", "from", sessionID, "to", nextID)
		sess = nextSess
		sessionID = nextID
	}

	http.Error(w, "Torrent playback failed: "+errorString(lastErr), http.StatusGatewayTimeout)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) deriveNextSlotID(ctx context.Context, currentID string, stream *auth.Stream) (string, error) {
	streamId, contentType, id, currentIndex, ok := parseStreamSlotID(currentID)
	if !ok {
		return "", nil
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return "", err
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil {
		return "", nil
	}
	return s.deriveNextSlotIDFromPlaylist(currentID, key, currentIndex, list, stream), nil
}

func (s *Server) deriveNextSlotIDFromPlaylist(currentID string, key StreamSlotKey, currentIndex int, list *playlistResult, stream *auth.Stream) string {
	n := len(list.Candidates)
	useSlotPaths := len(list.SlotPaths) == n
	startIndex := currentIndex + 1
	if useSlotPaths {
		foundCurrent := false
		for i, slotPath := range list.SlotPaths {
			if slotPath == currentID {
				startIndex = i + 1
				foundCurrent = true
				break
			}
		}
		if !foundCurrent {
			startIndex = 0
			for i, slotPath := range list.SlotPaths {
				_, _, _, slotIndex, ok := parseStreamSlotID(slotPath)
				if !ok {
					continue
				}
				if slotIndex <= currentIndex {
					startIndex = i + 1
					continue
				}
				break
			}
		}
		if startIndex > n {
			return ""
		}
	}

	// Sequential fallback: increment index, skip slots already marked as failed.
	for i := startIndex; i < n; i++ {
		slotPath := key.SlotPath(i)
		if useSlotPaths {
			slotPath = list.SlotPaths[i]
		}
		if !s.sessionManager.GetSlotFailedDuringPlayback(slotPath) {
			return slotPath
		}
	}
	return ""
}

// markSessionServedSuccessfully records that a slot actually delivered enough
// video bytes to count as a successful play. Used by the /next cursor to know
// the user is intentionally advancing rather than retrying a stuck slot.
func (s *Server) markSessionServedSuccessfully(sessionID string, sess *session.Session) {
	if _, already := s.recordedSuccessSessionIDs.LoadOrStore(sessionID, struct{}{}); already {
		return
	}
	// When the session is evicted, clear the key so future plays get a fresh state.
	go func(id string, done <-chan struct{}) {
		if done == nil {
			return
		}
		<-done
		s.recordedSuccessSessionIDs.Delete(id)
	}(sessionID, sess.Done())
}
