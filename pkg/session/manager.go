// Package session tracks playback sessions for the Stremio addon.
//
// A session maps a play slot (URL path segment) to the torrent release chosen
// for it, plus lifecycle bookkeeping: active plays, last access, and eviction.
// The actual media transfer is handled by the torrent manager (qBittorrent on
// a seedbox); sessions only carry metadata and playback state.
package session

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/release"
)

// ContentMeta identifies the content a session plays (for Cerberus and logging).
type ContentMeta struct {
	ImdbID  string
	TmdbID  string
	TvdbID  string
	Season  int
	Episode int
}

// Session is one playback slot: the release selected for it plus lifecycle state.
type Session struct {
	ID         string
	StreamName string

	CreatedAt           time.Time
	LastAccess          time.Time
	ActivePlays         int32
	PlaybackValidatedAt time.Time // when playback proved the file is playable
	PlaybackStartedAt   time.Time // when ActivePlays went from 0 to >0; used to evict stuck sessions
	PlaybackEndedAt     time.Time // when ActivePlays went to 0; used to evict session soon after stream stops
	playbackStarting    int       // in-flight playback startups; prevents replacing the session mid-startup
	Clients             map[string]time.Time
	mu                  sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	Release *release.Release

	ContentIDs *ContentMeta

	// ContentType and ContentID are the request context (e.g. "movie"/"series"
	// and "tt123" or "tmdb:123:1:2").
	ContentType          string
	ContentID            string
	ContentTitle         string
	selectedPlaybackFile string

	bytesRead atomic.Int64

	// position reports where the viewer currently is, supplied by whatever is
	// serving the bytes. Held as a function rather than a value so the dashboard
	// reads a live figure instead of whatever was last pushed, and so this
	// package stays independent of how playback is implemented.
	position atomic.Pointer[func() PlaybackPosition]
}

// PlaybackPosition is where the viewer is in the title and how much data lies
// between them and the end of what has been downloaded.
type PlaybackPosition struct {
	ByteOffset      int64   `json:"byte_offset"`
	FileSize        int64   `json:"file_size"`
	Percent         float64 `json:"percent"`
	PositionSeconds int64   `json:"position_seconds"`
	RuntimeSeconds  int64   `json:"runtime_seconds"`
	RunwayBytes     int64   `json:"runway_bytes"`
	RunwaySeconds   int64   `json:"runway_seconds"`
	Seeks           int64   `json:"seeks"`
}

// SetPositionSource registers where to read the live playback position from.
// Called when a stream starts serving; passing nil clears it.
func (s *Session) SetPositionSource(fn func() PlaybackPosition) {
	if s == nil {
		return
	}
	if fn == nil {
		s.position.Store(nil)
		return
	}
	s.position.Store(&fn)
}

// Position returns the live playback position, and false when nothing is
// currently serving this session.
func (s *Session) Position() (PlaybackPosition, bool) {
	if s == nil {
		return PlaybackPosition{}, false
	}
	fn := s.position.Load()
	if fn == nil {
		return PlaybackPosition{}, false
	}
	return (*fn)(), true
}

// Done returns a channel that is closed when the session is closed (e.g. user
// closed it from the dashboard). Use with the request context so playback
// aborts when either the client disconnects or the session is closed.
func (s *Session) Done() <-chan struct{} {
	if s == nil || s.ctx == nil {
		return nil
	}
	return s.ctx.Done()
}

func (s *Session) Context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// ReleaseURL returns the release link for logging/reporting.
func (s *Session) ReleaseURL() string {
	if s == nil || s.Release == nil {
		return ""
	}
	return s.Release.Link
}

// ReportSize returns the release size for reporting.
func (s *Session) ReportSize() int64 {
	if s == nil || s.Release == nil {
		return 0
	}
	return s.Release.Size
}

// ReportReleaseName returns the release title for reporting.
func (s *Session) ReportReleaseName() string {
	if s == nil || s.Release == nil {
		return ""
	}
	return s.Release.Title
}

// ReleaseIndexer returns the tracker name the release came from.
func (s *Session) ReleaseIndexer() string {
	if s == nil || s.Release == nil {
		return ""
	}
	return s.Release.Indexer
}

func (s *Session) BytesRead() int64 {
	return s.bytesRead.Load()
}

// AddBytesRead adds n to the session's bytes-read counter.
func (s *Session) AddBytesRead(n int64) {
	if n > 0 {
		s.bytesRead.Add(n)
	}
}

// IsActivelyServing returns true if at least one goroutine is currently
// serving this session (i.e. http.ServeContent is running).
func (s *Session) IsActivelyServing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ActivePlays > 0
}

// HasPreviouslyServed returns true if this session has already validated its
// playback source.
func (s *Session) HasPreviouslyServed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.PlaybackValidatedAt.IsZero()
}

func (s *Session) SetSelectedPlaybackFile(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectedPlaybackFile = name
}

func (s *Session) SelectedPlaybackFile() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedPlaybackFile
}

func (s *Session) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
}

// MaxPlaybackDuration is the maximum time a session can stay in "active playback"
// before being evicted even if EndPlayback was never called (e.g. stuck connection).
const MaxPlaybackDuration = 6 * time.Hour

// FailoverOrderTTL is how long stream failover order entries are kept before expiry.
const FailoverOrderTTL = 24 * time.Hour

// PostPlaybackEvictTTL is how long a session stays in memory after playback ends
// before being evicted. Long enough that pause/resume does not need a new search.
const PostPlaybackEvictTTL = 15 * time.Minute

// clientStaleTTL is how long a Clients map entry is kept before it is treated as stale.
const clientStaleTTL = 60 * time.Second

// deferredSessionReplaceGrace is a short protection window after a session was last
// touched. It avoids replacing a slot that was just handed to playback while startup
// bookkeeping is still catching up.
const deferredSessionReplaceGrace = 5 * time.Second

type failoverOrderEntry struct {
	order     []string
	expiresAt time.Time
}

type failedSlotEntry struct {
	expiresAt time.Time
}

// Manager owns the session map and its eviction loop.
type Manager struct {
	sessions                 map[string]*Session
	ttl                      time.Duration
	postPlaybackEvictTTL     time.Duration
	maxPlaybackDuration      time.Duration
	mu                       sync.RWMutex
	failoverOrder            sync.Map
	slotFailedDuringPlayback sync.Map // slotPath -> *failedSlotEntry
	stopCh                   chan struct{}
}

func NewManager(ttl time.Duration) *Manager {
	m := &Manager{
		sessions:             make(map[string]*Session),
		ttl:                  ttl,
		postPlaybackEvictTTL: PostPlaybackEvictTTL,
		maxPlaybackDuration:  MaxPlaybackDuration,
		stopCh:               make(chan struct{}),
	}
	go m.cleanupLoop()
	return m
}

func (m *Manager) SetTTL(d time.Duration) {
	if d > 0 {
		m.ttl = d
	}
}

func (m *Manager) SetPostPlaybackEvictTTL(d time.Duration) {
	if d > 0 {
		m.postPlaybackEvictTTL = d
	}
}

func (m *Manager) Shutdown() {
	select {
	case <-m.stopCh:
		// already closed
	default:
		close(m.stopCh)
	}
}

// DeferredSessionCreateOutcome describes what CreateDeferredSession did.
type DeferredSessionCreateOutcome string

const (
	DeferredSessionCreateCreated  DeferredSessionCreateOutcome = "created"
	DeferredSessionCreateExisting DeferredSessionCreateOutcome = "existing"
	DeferredSessionCreateReplaced DeferredSessionCreateOutcome = "replaced"
)

func newSession(sessionID string, rel *release.Release, contentIDs *ContentMeta, contentType, contentID, contentTitle, streamName string) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	return &Session{
		ID:           sessionID,
		StreamName:   streamName,
		CreatedAt:    now,
		LastAccess:   now,
		Clients:      make(map[string]time.Time),
		ctx:          ctx,
		cancel:       cancel,
		Release:      rel,
		ContentIDs:   contentIDs,
		ContentType:  contentType,
		ContentID:    contentID,
		ContentTitle: contentTitle,
	}
}

// CreateDeferredSession registers (or refreshes) the session for a play slot.
// If a session already exists for the slot with the same release it is kept;
// if the release changed and the old session is idle it is replaced.
func (m *Manager) CreateDeferredSession(sessionID string, rel *release.Release, contentIDs *ContentMeta, contentType, contentID, contentTitle, streamName string) (*Session, DeferredSessionCreateOutcome, error) {
	if sessionID == "" {
		return nil, "", fmt.Errorf("session ID is required")
	}
	if rel == nil {
		return nil, "", fmt.Errorf("release is required")
	}
	streamName = strings.TrimSpace(streamName)
	if streamName == "" {
		streamName = "default"
	}
	originalID := sessionID
	canonicalID, err := canonicalSessionIDForStream(originalID, streamName)
	if err != nil {
		return nil, "", err
	}
	sessionID = canonicalID

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.sessions[sessionID]
	if !ok {
		legacyID := ""
		if sessionID != originalID {
			legacyID = originalID
		} else {
			legacyID = legacyOwnerlessSlotID(originalID, streamName)
		}
		if legacyID != "" {
			if legacy, found := m.sessions[legacyID]; found && legacy != nil {
				legacy.mu.Lock()
				legacyOwner := legacy.StreamName
				if legacyOwner != "" && legacyOwner != "default" && legacyOwner != streamName {
					legacy.mu.Unlock()
					return nil, "", fmt.Errorf("session belongs to another stream")
				}
				legacy.ID = sessionID
				legacy.StreamName = streamName
				legacy.mu.Unlock()
				delete(m.sessions, legacyID)
				m.sessions[sessionID] = legacy
				existing, ok = legacy, true
			}
		}
	}
	if ok && existing != nil {
		existing.mu.Lock()
		if existing.StreamName != "" && existing.StreamName != streamName {
			existing.mu.Unlock()
			return nil, "", fmt.Errorf("session belongs to another stream")
		}
		if existing.StreamName == "" {
			existing.StreamName = streamName
		}
		sameRelease := existing.Release != nil && existing.Release.Link == rel.Link && existing.Release.Title == rel.Title
		busy := existing.ActivePlays > 0 || existing.playbackStarting > 0 ||
			time.Since(existing.LastAccess) < deferredSessionReplaceGrace
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		if sameRelease || busy {
			return existing, DeferredSessionCreateExisting, nil
		}
		// Release changed and the old session is idle: replace it.
		existing.Close()
		sess := newSession(sessionID, rel, contentIDs, contentType, contentID, contentTitle, streamName)
		m.sessions[sessionID] = sess
		return sess, DeferredSessionCreateReplaced, nil
	}

	sess := newSession(sessionID, rel, contentIDs, contentType, contentID, contentTitle, streamName)
	m.sessions[sessionID] = sess
	return sess, DeferredSessionCreateCreated, nil
}

func (m *Manager) GetSession(sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	session.LastAccess = time.Now()
	session.mu.Unlock()

	return session, nil
}

// GetSessionForStream returns a session only when it belongs to streamName.
// Slot IDs are externally supplied, so callers serving an addon request must
// use this lookup instead of GetSession to prevent one stream from reusing
// another stream's playback session.
func (m *Manager) GetSessionForStream(sessionID, streamName string) (*Session, error) {
	streamName = strings.TrimSpace(streamName)
	if streamName == "" {
		return nil, fmt.Errorf("stream owner is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	requestedID := sessionID
	canonicalID := canonicalOwnerlessSlotID(sessionID, streamName)
	if canonicalID != "" {
		requestedID = canonicalID
	}
	sess, ok := m.sessions[requestedID]
	if !ok {
		legacyID := sessionID
		if canonicalID == "" {
			legacyID = legacyOwnerlessSlotID(sessionID, streamName)
		}
		if legacyID != "" {
			sess, ok = m.sessions[legacyID]
			if ok && sess != nil {
				sess.mu.Lock()
				legacyOwner := sess.StreamName
				if legacyOwner != "" && legacyOwner != "default" && legacyOwner != streamName {
					sess.mu.Unlock()
					ok = false
				} else {
					sess.ID = requestedID
					sess.StreamName = streamName
					sess.mu.Unlock()
					delete(m.sessions, legacyID)
					m.sessions[requestedID] = sess
				}
			}
		}
	}
	if !ok || sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.StreamName != streamName {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	sess.LastAccess = time.Now()
	return sess, nil
}

func canonicalOwnerlessSlotID(sessionID, streamName string) string {
	const prefix = "stream:"
	if !strings.HasPrefix(sessionID, prefix) || streamName == "" {
		return ""
	}
	rest := strings.TrimPrefix(sessionID, prefix)
	if !strings.HasPrefix(rest, "movie:") && !strings.HasPrefix(rest, "series:") {
		return ""
	}
	return prefix + streamName + ":" + rest
}

func canonicalSessionIDForStream(sessionID, streamName string) (string, error) {
	const prefix = "stream:"
	if !strings.HasPrefix(sessionID, prefix) {
		return sessionID, nil
	}
	if canonicalID := canonicalOwnerlessSlotID(sessionID, streamName); canonicalID != "" {
		return canonicalID, nil
	}
	rest := strings.TrimPrefix(sessionID, prefix)
	parts := strings.Split(rest, ":")
	if len(parts) >= 3 && (parts[1] == "movie" || parts[1] == "series") {
		if parts[0] != streamName {
			return "", fmt.Errorf("session slot belongs to another stream")
		}
	}
	return sessionID, nil
}

func legacyOwnerlessSlotID(sessionID, streamName string) string {
	const prefix = "stream:"
	if !strings.HasPrefix(sessionID, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(sessionID, prefix)
	ownerPrefix := streamName + ":"
	if !strings.HasPrefix(rest, ownerPrefix) {
		return ""
	}
	legacyRest := strings.TrimPrefix(rest, ownerPrefix)
	if len(strings.Split(legacyRest, ":")) < 3 {
		return ""
	}
	return prefix + legacyRest
}

func (m *Manager) DeleteSession(sessionID string) {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if ok && session != nil {
		session.Close()
		logger.Debug("Session deleted", "session", sessionID)
	}
}

// HasActiveSessionForContentID returns true if any session with the given content
// type and content ID exists in the session map (regardless of whether it is
// actively serving). Used to detect Stremio's next-episode preload.
func (m *Manager) HasActiveSessionForContentID(contentType, contentID string) bool {
	if contentType == "" || contentID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		s.mu.Lock()
		ct := s.ContentType
		cid := s.ContentID
		s.mu.Unlock()
		if ct == contentType && cid == contentID {
			return true
		}
	}
	return false
}

func (m *Manager) StartPlayback(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.ActivePlays == 0 {
			s.PlaybackStartedAt = time.Now()
		}
		s.ActivePlays++
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
}

func (m *Manager) EndPlayback(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.ActivePlays > 0 {
			s.ActivePlays--
		}
		if s.ActivePlays == 0 {
			s.PlaybackStartedAt = time.Time{}
			s.PlaybackEndedAt = time.Now() // so cleanup can evict after postPlaybackEvictTTL
		}
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
}

func (m *Manager) MarkPlaybackValidated(id string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.PlaybackValidatedAt.IsZero() {
			s.PlaybackValidatedAt = time.Now()
		}
		s.playbackStarting = 0
		s.mu.Unlock()
	}
}

func (m *Manager) BeginPlaybackStartup(id string) {
	now := time.Now()
	m.mu.RLock()
	s := m.sessions[id]
	if s != nil {
		s.mu.Lock()
		s.playbackStarting++
		s.LastAccess = now
		s.mu.Unlock()
	}
	m.mu.RUnlock()
}

func (m *Manager) EndPlaybackStartup(id string) {
	now := time.Now()
	m.mu.RLock()
	s := m.sessions[id]
	if s != nil {
		s.mu.Lock()
		if s.playbackStarting > 0 {
			s.playbackStarting--
		}
		s.LastAccess = now
		s.mu.Unlock()
	}
	m.mu.RUnlock()
}

func failoverOrderMapKey(streamToken, streamKey string) string {
	if streamKey == "" {
		return streamToken
	}
	return streamToken + "|" + streamKey
}

func (m *Manager) SetStreamFailoverOrder(streamToken, streamKey string, order []string) {
	if len(order) == 0 {
		return
	}
	cp := make([]string, len(order))
	copy(cp, order)
	m.failoverOrder.Store(failoverOrderMapKey(streamToken, streamKey), &failoverOrderEntry{
		order:     cp,
		expiresAt: time.Now().Add(FailoverOrderTTL),
	})
}

func ownerFailoverOrderMapKey(streamToken, streamName, streamKey string) string {
	return "owner|" + streamName + "|" + streamToken + "|" + streamKey
}

// SetStreamFailoverOrderForStream stores failover order under the authenticated
// stream as well as the legacy token/key namespace.
func (m *Manager) SetStreamFailoverOrderForStream(streamToken, streamName, streamKey string, order []string) {
	streamName = strings.TrimSpace(streamName)
	if streamName == "" || len(order) == 0 {
		return
	}
	cp := append([]string(nil), order...)
	m.failoverOrder.Store(ownerFailoverOrderMapKey(streamToken, streamName, streamKey), &failoverOrderEntry{
		order:     cp,
		expiresAt: time.Now().Add(FailoverOrderTTL),
	})
}

// GetStreamFailoverOrder returns the stored failover order for this stream token
// and stream key. It tries key-specific storage first, then falls back to
// token-only (legacy). Returns nil if the entry is missing or expired.
func (m *Manager) GetStreamFailoverOrder(streamToken, streamKey string) []string {
	now := time.Now()
	if streamKey != "" {
		if val, ok := m.failoverOrder.Load(failoverOrderMapKey(streamToken, streamKey)); ok && val != nil {
			if ent, ok := val.(*failoverOrderEntry); ok && ent != nil && now.Before(ent.expiresAt) {
				return ent.order
			}
		}
	}
	val, ok := m.failoverOrder.Load(streamToken)
	if !ok || val == nil {
		return nil
	}
	ent, ok := val.(*failoverOrderEntry)
	if !ok || ent == nil || now.After(ent.expiresAt) {
		return nil
	}
	return ent.order
}

// GetStreamFailoverOrderForStream prefers owner-qualified state and falls back
// to the token-only methods for legacy clients that posted ownerless slots.
func (m *Manager) GetStreamFailoverOrderForStream(streamToken, streamName, streamKey string) []string {
	streamName = strings.TrimSpace(streamName)
	if streamName != "" {
		if val, ok := m.failoverOrder.Load(ownerFailoverOrderMapKey(streamToken, streamName, streamKey)); ok && val != nil {
			if ent, ok := val.(*failoverOrderEntry); ok && ent != nil && time.Now().Before(ent.expiresAt) {
				return append([]string(nil), ent.order...)
			}
		}
	}
	if order := m.GetStreamFailoverOrder(streamToken, streamKey); len(order) > 0 {
		return order
	}
	legacyKeyPrefix := streamName + ":"
	if streamName != "default" && strings.HasPrefix(streamKey, legacyKeyPrefix) {
		legacyKey := "default:" + strings.TrimPrefix(streamKey, legacyKeyPrefix)
		return m.GetStreamFailoverOrder(streamToken, legacyKey)
	}
	return nil
}

// SetSlotFailedDuringPlayback marks the slot as having failed during playback.
// Subsequent play requests for this slot should redirect to the next fallback.
func (m *Manager) SetSlotFailedDuringPlayback(slotPath string) {
	if slotPath == "" {
		return
	}
	expiresAt := time.Now().Add(m.ttl)
	m.slotFailedDuringPlayback.Store(slotPath, &failedSlotEntry{expiresAt: expiresAt})
}

// GetSlotFailedDuringPlayback returns true if this slot recently failed during playback.
func (m *Manager) GetSlotFailedDuringPlayback(slotPath string) bool {
	val, ok := m.slotFailedDuringPlayback.Load(slotPath)
	if !ok || val == nil {
		return false
	}
	ent, ok := val.(*failedSlotEntry)
	if !ok || ent == nil || time.Now().After(ent.expiresAt) {
		return false
	}
	return true
}

func (m *Manager) ClearSlotFailedDuringPlayback(slotPath string) {
	if slotPath == "" {
		return
	}
	m.slotFailedDuringPlayback.Delete(slotPath)
}

// GetSlotFailedDuringPlaybackForStream also checks the pre-stream-qualified
// slot namespace and migrates a legacy failure to the canonical owner path.
func (m *Manager) GetSlotFailedDuringPlaybackForStream(slotPath, streamName string) bool {
	if m.GetSlotFailedDuringPlayback(slotPath) {
		return true
	}
	legacyID := legacyOwnerlessSlotID(slotPath, strings.TrimSpace(streamName))
	if legacyID == "" || !m.GetSlotFailedDuringPlayback(legacyID) {
		return false
	}
	m.SetSlotFailedDuringPlayback(slotPath)
	m.slotFailedDuringPlayback.Delete(legacyID)
	return true
}

func (m *Manager) ClearSlotFailedDuringPlaybackForStream(slotPath, streamName string) {
	m.ClearSlotFailedDuringPlayback(slotPath)
	if legacyID := legacyOwnerlessSlotID(slotPath, strings.TrimSpace(streamName)); legacyID != "" {
		m.ClearSlotFailedDuringPlayback(legacyID)
	}
}

// ActiveSessionInfo is the dashboard view of one actively-playing session.
type ActiveSessionInfo struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Clients   []string `json:"clients"`
	StartTime string   `json:"start_time"`
	// Position is nil when nothing is currently serving bytes for this session.
	Position *PlaybackPosition `json:"position,omitempty"`
}

func (m *Manager) GetActiveSessions() []ActiveSessionInfo {
	m.mu.RLock()
	snapshot := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		snapshot = append(snapshot, s)
	}
	m.mu.RUnlock()

	var result []ActiveSessionInfo
	for _, s := range snapshot {
		if !s.mu.TryLock() {
			continue
		}
		for ip, lastSeen := range s.Clients {
			if time.Since(lastSeen) > clientStaleTTL {
				delete(s.Clients, ip)
			}
		}
		if len(s.Clients) > 0 {
			clients := make([]string, 0, len(s.Clients))
			for ip := range s.Clients {
				clients = append(clients, ip)
			}
			title := "Unknown"
			if s.Release != nil && s.Release.Title != "" {
				title = s.Release.Title
			}
			info := ActiveSessionInfo{
				ID:        s.ID,
				Title:     title,
				Clients:   clients,
				StartTime: s.CreatedAt.Format(time.Kitchen),
			}
			if pos, ok := s.Position(); ok {
				info.Position = &pos
			}
			result = append(result, info)
		}
		s.mu.Unlock()
	}
	return result
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *Manager) cleanup() {
	m.mu.Lock()

	now := time.Now()
	var toClose []*Session
	var idleEvictions int
	var postPlaybackEvictions int
	var stuckPlaybackEvictions int
	for id, session := range m.sessions {
		session.mu.Lock()
		// Evict stale Clients entries before computing hasActivePlayback so a
		// disconnected client can't block eviction indefinitely.
		for ip, lastSeen := range session.Clients {
			if now.Sub(lastSeen) > clientStaleTTL {
				delete(session.Clients, ip)
			}
		}
		hasActivePlayback := session.ActivePlays > 0 || len(session.Clients) > 0
		evictIdle := !hasActivePlayback && now.Sub(session.LastAccess) > m.ttl
		evictPostPlayback := !hasActivePlayback && !session.PlaybackEndedAt.IsZero() && now.Sub(session.PlaybackEndedAt) > m.postPlaybackEvictTTL
		evictStuckPlayback := hasActivePlayback && !session.PlaybackStartedAt.IsZero() && now.Sub(session.PlaybackStartedAt) > m.maxPlaybackDuration
		if evictIdle || evictPostPlayback || evictStuckPlayback {
			delete(m.sessions, id)
			toClose = append(toClose, session)
			switch {
			case evictStuckPlayback:
				stuckPlaybackEvictions++
			case evictPostPlayback:
				postPlaybackEvictions++
			default:
				idleEvictions++
			}
		}
		session.mu.Unlock()
	}
	m.mu.Unlock()

	for _, s := range toClose {
		s.Close()
	}
	if len(toClose) > 0 {
		logger.Debug(
			"session cleanup evicted sessions",
			"count", len(toClose),
			"idle", idleEvictions,
			"post_playback", postPlaybackEvictions,
			"stuck_playback", stuckPlaybackEvictions,
		)
		go freeOSMemory()
	}

	m.slotFailedDuringPlayback.Range(func(key, val any) bool {
		if ent, ok := val.(*failedSlotEntry); ok && now.After(ent.expiresAt) {
			m.slotFailedDuringPlayback.Delete(key)
		}
		return true
	})
	m.failoverOrder.Range(func(key, val any) bool {
		if ent, ok := val.(*failoverOrderEntry); ok && now.After(ent.expiresAt) {
			m.failoverOrder.Delete(key)
		}
		return true
	})
}

// freeOSMemory runs GC and returns unused memory to the OS so RSS drops after
// session close.
func freeOSMemory() {
	runtime.GC()
	debug.FreeOSMemory()
}
