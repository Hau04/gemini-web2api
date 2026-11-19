package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Attachment struct {
	Filename   string `json:"filename"`
	MIME       string `json:"mime"`
	DataBase64 string `json:"data_base64"`
	Size       int    `json:"size,omitempty"`
	Source     string `json:"source,omitempty"`
}

type Request struct {
	ID          string       `json:"id"`
	Prompt      string       `json:"prompt"`
	Model       string       `json:"model,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	NewChat     bool         `json:"new_chat,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

type GenerateOptions struct {
	Prompt      string
	Model       string
	Attachments []Attachment
	NewChat     bool
	LoopPenalty bool
}

type Chunk struct {
	ID          string `json:"id"`
	Text        string `json:"text,omitempty"`
	Done        bool   `json:"done,omitempty"`
	Error       string `json:"error,omitempty"`
	RateLimited bool   `json:"rate_limited,omitempty"`
	// Status carries the content script's current turn phase on keepalive
	// (empty-text) chunks: "starting", "new-chat", "model-select", "upload",
	// "paste", "submit", "generating". The server uses it to tell a working tab
	// (phase advances / generating) from a stuck one during long no-content phases.
	Status      string `json:"status,omitempty"`
}

// PausedError is returned when the user temporarily pauses the bridge from the
// Chrome extension popup (e.g. to stop a runaway agent loop).
type PausedError struct{}

func (e *PausedError) Error() string {
	return "browser bridge is paused by user (click Resume Bridge in the extension popup)"
}

// PacingSnapshot summarizes live anti-ban pacing counters and bridge pause/cooldown state.
type PacingSnapshot struct {
	Recent10m   int
	MaxPer10m   int
	Recent1h    int
	MaxPerHour  int
	CooldownSec int
	Paused      bool
}

// RateLimitError is returned when the bridge circuit breaker is cooling down
// after Google signaled HTTP 429 / quota / unusual traffic.
type RateLimitError struct {
	RetryAfter time.Duration
	Cause      string
}

func (e *RateLimitError) Error() string {
	secs := int(e.RetryAfter.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	if e.Cause != "" {
		return fmt.Sprintf("bridge rate-limit cooldown active (%s); retry after %ds", e.Cause, secs)
	}
	return fmt.Sprintf("bridge rate-limit cooldown active; retry after %ds", secs)
}

func looksLikeRateLimitError(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "http 429") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "quota exceeded") ||
		strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "unusual traffic")
}

type inFlightEntry struct {
	req        *Request
	tabName    string
	clientID   string
	assignedAt time.Time
	hasChunks  bool
}

type tabSession struct {
	convGen          string
	fingerprints     []string
	userFingerprints []string
	toolNames        map[string]bool
	uploadedFiles    map[string]string // canonical path or "sha256:..." -> signature
}

type persistedPacingState struct {
	RecentDispatches      []int64 `json:"recent_dispatches_unix_ms,omitempty"`
	CooldownUntilUnixMs   int64   `json:"cooldown_until_unix_ms,omitempty"`
	ConsecutiveRateLimits int     `json:"consecutive_rate_limits,omitempty"`
	LastRateLimitCause    string  `json:"last_rate_limit_cause,omitempty"`
}

type Hub struct {
	mu                    sync.Mutex
	dispatchGate          chan struct{}
	seq                   atomic.Uint64
	tabName               string
	lastHeartbeat         time.Time
	frozen                atomic.Bool
	degradedUntil         time.Time
	chatModeOverride      string
	noContentTimeout      time.Duration
	clientHeartbeats      map[string]time.Time
	pending               []*Request
	inFlight              map[string]*inFlightEntry
	waiters               map[string]chan Chunk
	pollNotify            chan struct{}
	minInterval           time.Duration
	jitterMax             time.Duration
	maxPer10Min           int
	maxPerHour            int
	paused                bool
	pacingStatePath       string
	recentDispatches      []time.Time
	lastCompletedAt       time.Time
	cooldownUntil         time.Time
	lastRateLimitCause    string
	consecutiveRateLimits int
	activeClientID        string
	activeConvGen         string
	sessions              map[string]*tabSession
}

func NewHub() *Hub {
	return &Hub{
		dispatchGate:     make(chan struct{}, 1),
		clientHeartbeats: make(map[string]time.Time),
		inFlight:         make(map[string]*inFlightEntry),
		waiters:          make(map[string]chan Chunk),
		pollNotify:       make(chan struct{}, 1),
		sessions:         make(map[string]*tabSession),
	}
}

func (h *Hub) ensureSessionLocked(clientID string) *tabSession {
	sess, ok := h.sessions[clientID]
	if !ok {
		sess = &tabSession{
			uploadedFiles: make(map[string]string),
		}
		h.sessions[clientID] = sess
	}
	return sess
}

// SetMinInterval configures the anti-ban minimum cooldown between consecutive
// browser-bridge generations to avoid rapid-fire automated request bursts.
func (h *Hub) SetMinInterval(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d < 0 {
		d = 0
	}
	h.minInterval = d
}

// SetPacing configures anti-ban pacing: base cooldown, human-like random jitter,
// and maximum prompts allowed per rolling 10-minute window (0 disables cap).
func (h *Hub) SetPacing(minInterval, jitter time.Duration, maxPer10Min int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if minInterval < 0 {
		minInterval = 0
	}
	if jitter < 0 {
		jitter = 0
	}
	if maxPer10Min < 0 {
		maxPer10Min = 0
	}
	h.minInterval = minInterval
	h.jitterMax = jitter
	h.maxPer10Min = maxPer10Min
}

// SetHourlyLimit configures the optional rolling 1-hour maximum prompt cap (0 disables).
func (h *Hub) SetHourlyLimit(maxPerHour int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if maxPerHour < 0 {
		maxPerHour = 0
	}
	h.maxPerHour = maxPerHour
}

// SetPacingStateFile enables persistent anti-ban pacing & cooldown survival across restarts
// and immediately restores any unexpired timestamps or active 429 cooldown from disk.
func (h *Hub) SetPacingStateFile(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pacingStatePath = strings.TrimSpace(path)
	if h.pacingStatePath == "" {
		return
	}
	h.loadPacingStateLocked()
}

func (h *Hub) loadPacingStateLocked() {
	if h.pacingStatePath == "" {
		return
	}
	raw, err := os.ReadFile(h.pacingStatePath)
	if err != nil || len(raw) == 0 {
		return
	}
	var st persistedPacingState
	if err := json.Unmarshal(raw, &st); err != nil {
		return
	}
	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)
	for _, ms := range st.RecentDispatches {
		ts := time.UnixMilli(ms)
		if ts.After(cutoff) && !ts.After(now.Add(1*time.Minute)) {
			h.recentDispatches = append(h.recentDispatches, ts)
		}
	}
	sort.Slice(h.recentDispatches, func(i, j int) bool {
		return h.recentDispatches[i].Before(h.recentDispatches[j])
	})
	h.pruneRecentLocked(now)
	if st.CooldownUntilUnixMs > 0 {
		until := time.UnixMilli(st.CooldownUntilUnixMs)
		if until.After(now) && until.After(h.cooldownUntil) {
			h.cooldownUntil = until
			h.consecutiveRateLimits = st.ConsecutiveRateLimits
			if st.LastRateLimitCause != "" {
				h.lastRateLimitCause = st.LastRateLimitCause
			}
		}
	}
}

func (h *Hub) savePacingStateLocked() {
	if h.pacingStatePath == "" {
		return
	}
	now := time.Now()
	h.pruneRecentLocked(now)
	st := persistedPacingState{
		RecentDispatches:      make([]int64, 0, len(h.recentDispatches)),
		ConsecutiveRateLimits: h.consecutiveRateLimits,
		LastRateLimitCause:    h.lastRateLimitCause,
	}
	for _, ts := range h.recentDispatches {
		st.RecentDispatches = append(st.RecentDispatches, ts.UnixMilli())
	}
	if h.cooldownUntil.After(now) {
		st.CooldownUntilUnixMs = h.cooldownUntil.UnixMilli()
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	dir := filepath.Dir(h.pacingStatePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := h.pacingStatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		if renameErr := os.Rename(tmp, h.pacingStatePath); renameErr != nil {
			_ = os.WriteFile(h.pacingStatePath, b, 0o600)
			_ = os.Remove(tmp)
		}
	}
}

// SetPaused pauses or resumes browser-bridge request dispatching (controlled from popup).
func (h *Hub) SetPaused(paused bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.paused = paused
}

// IsPaused returns whether the user has paused the bridge.
func (h *Hub) IsPaused() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.paused
}

// RecordRateLimit trips the exponential cool-off circuit breaker (60s -> 180s -> 300s).
func (h *Hub) RecordRateLimit(cause string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.recordRateLimitLocked(cause)
}

func (h *Hub) recordRateLimitLocked(cause string) time.Duration {
	h.consecutiveRateLimits++
	var backoff time.Duration
	switch h.consecutiveRateLimits {
	case 1:
		backoff = 60 * time.Second
	case 2:
		backoff = 180 * time.Second
	default:
		backoff = 300 * time.Second
	}
	until := time.Now().Add(backoff)
	if until.After(h.cooldownUntil) {
		h.cooldownUntil = until
	}
	if cause != "" {
		h.lastRateLimitCause = cause
	}
	h.savePacingStateLocked()
	return time.Until(h.cooldownUntil)
}

// CooldownRemaining returns any active rate-limit circuit-breaker remaining duration.
func (h *Hub) CooldownRemaining() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Now().Before(h.cooldownUntil) {
		return time.Until(h.cooldownUntil)
	}
	return 0
}

// SetFrozen records whether the browser tab is currently frozen by Chrome
// (Memory Saver / Energy Saver). A frozen tab cannot run the content script,
// so requests must route via the direct cookie mode instead. Transitioning to
// frozen also fails every in-flight turn immediately: the frozen tab will never
// finish them, and the error lets the server retry via direct cookie mode while
// the API client is still connected (instead of hanging until the client gives up).
func (h *Hub) SetFrozen(frozen bool) {
	h.frozen.Store(frozen)
	if frozen {
		h.failAllInFlight("browser tab frozen by Chrome (Memory Saver / Energy Saver) mid-turn — aborting so the server can retry via direct cookie mode")
	}
}

// failAllInFlight delivers an error chunk to every active waiter and drops
// the corresponding queued requests so a tab that wakes up later never re-runs a
// turn the server has already abandoned.
func (h *Hub) failAllInFlight(msg string) {
	h.mu.Lock()
	type waiter struct {
		id string
		ch chan Chunk
	}
	ws := make([]waiter, 0, len(h.waiters))
	for id, ch := range h.waiters {
		ws = append(ws, waiter{id: id, ch: ch})
		delete(h.inFlight, id)
	}
	remaining := h.pending[:0]
	for _, r := range h.pending {
		if _, abandoned := h.waiters[r.ID]; abandoned {
			continue
		}
		remaining = append(remaining, r)
	}
	h.pending = remaining
	h.mu.Unlock()

	for _, w := range ws {
		select {
		case w.ch <- Chunk{ID: w.id, Error: msg, Done: true}:
		default:
		}
	}
}

// IsFrozen reports whether the connected browser tab is frozen.
func (h *Hub) IsFrozen() bool {
	return h.frozen.Load()
}

// MarkDegraded marks the bridge as unreliable for d (e.g. after a generation
// timed out with zero chunks because the tab froze or stalled). While degraded,
// the server prefers the direct cookie mode when session cookies are available.
func (h *Hub) MarkDegraded(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	until := time.Now().Add(d)
	if until.After(h.degradedUntil) {
		h.degradedUntil = until
	}
}

// IsDegraded reports whether the bridge is within a degradation window.
func (h *Hub) IsDegraded() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().Before(h.degradedUntil)
}

// DegradedRemaining returns how long the degradation window has left (0 if none).
func (h *Hub) DegradedRemaining() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Now().Before(h.degradedUntil) {
		return time.Until(h.degradedUntil)
	}
	return 0
}

// ClearDegraded lifts the degradation window (called after a successful bridge
// turn or when the tab resumes from a frozen state).
func (h *Hub) ClearDegraded() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.degradedUntil = time.Time{}
}

// SetNoContentTimeout caps how long a bridge turn may run without any content
// (only empty keepalive chunks) before it is failed so the server can fall back
// to direct cookie mode. Keeps frozen/throttled tabs from hanging a request
// until the API client gives up. 0 disables the cap (legacy behavior).
func (h *Hub) SetNoContentTimeout(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.noContentTimeout = d
}

// SetChatMode stores the chat-session override controlled from the extension
// popup: "auto" (default heuristic), "new" (every request starts a fresh chat),
// or "continue" (every request appends to the tab's currently open chat).
func (h *Hub) SetChatMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "new" && mode != "continue" {
		mode = "auto"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.chatModeOverride = mode
}

// ChatMode returns the current chat-session override ("auto" when unset).
func (h *Hub) ChatMode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chatModeOverride == "" {
		return "auto"
	}
	return h.chatModeOverride
}

// IsConnected returns true if a browser tab sent a poll/heartbeat within the last 45 seconds,
// or if an active generation is still in flight within the 180s window.
// A frozen tab (Chrome Memory Saver / Energy Saver) never counts as connected.
func (h *Hub) IsConnected() bool {
	if h.frozen.Load() {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tabName == "" {
		return false
	}
	if time.Since(h.lastHeartbeat) < 45*time.Second {
		return true
	}
	return len(h.inFlight) > 0 && time.Since(h.lastHeartbeat) < 180*time.Second
}

func (h *Hub) TabName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tabName
}

func (h *Hub) Touch(tabName string) {
	h.Heartbeat(tabName, "")
}

// Heartbeat updates tab and client liveness timestamps.
func (h *Hub) Heartbeat(tabName, clientID string) {
	h.UpdateTabSession(tabName, clientID, "")
}

// UpdateTabSession records liveness and resets conversation delta state whenever
// a tab starts a fresh chat (new convGen), while preserving per-tab state so
// background heartbeats from other open tabs cannot wipe the active tab's uploads.
func (h *Hub) UpdateTabSession(tabName, clientID, convGen string) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if tabName != "" {
		h.tabName = tabName
	}

	key := clientID
	if key == "" {
		key = h.activeClientID
	}
	sess := h.ensureSessionLocked(key)

	if clientID != "" {
		h.clientHeartbeats[clientID] = now
	}
	if convGen != "" {
		if sess.convGen != "" && sess.convGen != convGen {
			sess.fingerprints = nil
			sess.userFingerprints = nil
			sess.toolNames = nil
			sess.uploadedFiles = make(map[string]string)
		}
		sess.convGen = convGen
	}

	// Do not let an idle background tab hijack activeClientID while another tab has an in-flight turn
	if clientID != "" {
		if h.activeClientID == "" || h.activeClientID == clientID || len(h.inFlight) == 0 {
			h.activeClientID = clientID
			h.activeConvGen = sess.convGen
		}
	} else if convGen != "" {
		h.activeConvGen = sess.convGen
	}
	h.lastHeartbeat = now
}

// ActiveSession snapshots the active tab identity and previously uploaded file signatures.
func (h *Hub) ActiveSession() (clientID, convGen string, uploaded map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(h.activeClientID)
	cp := make(map[string]string, len(sess.uploadedFiles))
	for k, v := range sess.uploadedFiles {
		cp[k] = v
	}
	return h.activeClientID, sess.convGen, cp
}

// MatchDeltaPrefix returns the number of leading messages already present in the
// active browser tab's live conversation if fingerprints strictly extend it.
func (h *Hub) MatchDeltaPrefix(fingerprints []string) int {
	matched, _ := h.MatchDeltaPrefixWithTools(fingerprints, nil)
	return matched
}

// MatchDeltaPrefixWithTools matches leading conversation fingerprints and also
// supports purely additive mid-chat tool additions (e.g. tools unlocked after ToolSearch):
// if all prior messages match and every previously declared tool is still present,
// returns the matched prefix alongside only the newly added tool names.
func (h *Hub) MatchDeltaPrefixWithTools(fingerprints []string, currentTools map[string]bool) (matchedPrefix int, addedTools []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(h.activeClientID)
	prev := sess.fingerprints
	if len(prev) == 0 || len(fingerprints) <= len(prev) {
		return 0, nil
	}
	// Check all message fingerprints (index 1..len(prev)-1) first
	for i := 1; i < len(prev); i++ {
		if prev[i] != fingerprints[i] {
			return 0, nil
		}
	}
	if prev[0] == fingerprints[0] {
		return len(prev), nil
	}

	// Tools digest changed: allow delta ONLY when tools were purely added (never removed/mutated).
	if len(sess.toolNames) == 0 || len(currentTools) <= len(sess.toolNames) {
		return 0, nil
	}
	for oldName := range sess.toolNames {
		if !currentTools[oldName] {
			return 0, nil
		}
	}
	for newName := range currentTools {
		if !sess.toolNames[newName] {
			addedTools = append(addedTools, newName)
		}
	}
	if len(addedTools) == 0 {
		return 0, nil
	}
	sort.Strings(addedTools)
	return len(prev), addedTools
}

// IsNewConversation returns true when the client (e.g. WorkBuddy) started a brand-new
// Turn-1 conversation while the open browser tab still holds a previous conversation.
// Never triggers on multi-turn continuations (messages with assistant/tool history) or
// identical Turn-1 retries.
func (h *Hub) IsNewConversation(incomingUserFPs []string, hasAssistantOrTool bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(h.activeClientID)
	if len(sess.userFingerprints) == 0 {
		return false
	}
	if hasAssistantOrTool || len(incomingUserFPs) != 1 {
		return false
	}
	if len(sess.userFingerprints) > 1 {
		return true
	}
	return incomingUserFPs[0] != sess.userFingerprints[0]
}

// RecordTurnSuccess saves the conversation prefix and uploaded file signatures for the
// tab session so subsequent follow-up turns can send compact deltas and avoid
// re-uploading unchanged files while automatically refreshing edited code files.
func (h *Hub) RecordTurnSuccess(clientID, convGen string, fingerprints []string, uploadedSigs map[string]string) {
	h.RecordTurnSuccessWithConversation(clientID, convGen, fingerprints, nil, uploadedSigs, false)
}

// RecordTurnSuccessWithConversation updates conversation fingerprints, user-message
// fingerprints (for New Chat detection), and uploaded-file signatures. When resetSession
// is true (the browser started a fresh chat), previous uploaded files are cleared first.
func (h *Hub) RecordTurnSuccessWithConversation(clientID, convGen string, fingerprints, userFingerprints []string, uploadedSigs map[string]string, resetSession bool) {
	h.RecordTurnSuccessWithTools(clientID, convGen, fingerprints, userFingerprints, nil, uploadedSigs, resetSession)
}

// RecordTurnSuccessWithTools additionally stores the declared tool names for the session
// so purely additive mid-chat tool updates can preserve delta context.
func (h *Hub) RecordTurnSuccessWithTools(clientID, convGen string, fingerprints, userFingerprints []string, toolNames map[string]bool, uploadedSigs map[string]string, resetSession bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.ensureSessionLocked(clientID)
	if convGen != "" && sess.convGen != "" && convGen != sess.convGen {
		return
	}
	if convGen != "" && sess.convGen == "" {
		sess.convGen = convGen
	}
	if resetSession {
		sess.uploadedFiles = make(map[string]string)
		sess.toolNames = nil
	}
	cp := make([]string, len(fingerprints))
	copy(cp, fingerprints)
	sess.fingerprints = cp
	if len(userFingerprints) > 0 {
		ucp := make([]string, len(userFingerprints))
		copy(ucp, userFingerprints)
		sess.userFingerprints = ucp
	}
	if len(toolNames) > 0 {
		tcp := make(map[string]bool, len(toolNames))
		for name := range toolNames {
			tcp[name] = true
		}
		sess.toolNames = tcp
	}
	for path, sig := range uploadedSigs {
		if path != "" {
			sess.uploadedFiles[path] = sig
		}
	}
}

// WaitForNextRequest blocks until a generation request is queued or timeout occurs.
func (h *Hub) WaitForNextRequest(ctx context.Context, tabName string, timeout time.Duration) (*Request, error) {
	return h.WaitForNextRequestForClient(ctx, tabName, "", timeout)
}

// WaitForNextRequestForClient blocks until a generation request is queued or timeout occurs,
// tracking the polling client ID to avoid duplicate dispatches across tabs.
func (h *Hub) WaitForNextRequestForClient(ctx context.Context, tabName, clientID string, timeout time.Duration) (*Request, error) {
	return h.WaitForNextRequestForSession(ctx, tabName, clientID, "", timeout)
}

// WaitForNextRequestForSession blocks until a generation request is queued or timeout occurs,
// tracking both clientID and conversation generation.
func (h *Hub) WaitForNextRequestForSession(ctx context.Context, tabName, clientID, convGen string, timeout time.Duration) (*Request, error) {
	h.UpdateTabSession(tabName, clientID, convGen)

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		h.mu.Lock()
		if len(h.pending) > 0 {
			req := h.pending[0]
			h.pending = h.pending[1:]
			if len(h.pending) == 0 {
				select {
				case <-h.pollNotify:
				default:
				}
			}
			h.inFlight[req.ID] = &inFlightEntry{
				req:        req,
				tabName:    tabName,
				clientID:   clientID,
				assignedAt: time.Now(),
			}
			h.mu.Unlock()
			return req, nil
		}

		// Only re-dispatch an orphaned in-flight request if zero chunks have been streamed yet
		// AND its assigned tab appears to have reloaded or stopped heartbeating.
		for id, entry := range h.inFlight {
			if _, exists := h.waiters[id]; !exists {
				delete(h.inFlight, id)
				continue
			}
			if entry.hasChunks {
				continue
			}
			orphaned := false
			if clientID != "" && entry.clientID != "" {
				if entry.clientID != clientID {
					lastSeen := h.clientHeartbeats[entry.clientID]
					if time.Since(lastSeen) > 15*time.Second && time.Since(entry.assignedAt) > 10*time.Second {
						orphaned = true
					}
				}
			} else if time.Since(entry.assignedAt) > 20*time.Second {
				orphaned = true
			}
			if orphaned {
				entry.tabName = tabName
				entry.clientID = clientID
				entry.assignedAt = time.Now()
				req := entry.req
				h.mu.Unlock()
				return req, nil
			}
		}

		notify := h.pollNotify
		h.mu.Unlock()

		select {
		case <-notify:
			// Re-check h.pending under lock; ignores stale or spurious wakeups.
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		}
	}
}

// SubmitChunk routes an incoming chunk from the browser extension to the waiting HTTP request.
func (h *Hub) SubmitChunk(c Chunk) error {
	h.Touch("")

	h.mu.Lock()
	if c.RateLimited || looksLikeRateLimitError(c.Error) {
		h.recordRateLimitLocked(c.Error)
		c.RateLimited = true
	} else if c.Done && c.Error == "" {
		if h.consecutiveRateLimits != 0 {
			h.consecutiveRateLimits = 0
			h.savePacingStateLocked()
		}
	}

	if entry, ok := h.inFlight[c.ID]; ok {
		if c.Text != "" {
			entry.hasChunks = true
		}
		if c.Done || c.Error != "" {
			delete(h.inFlight, c.ID)
		}
	}
	ch, exists := h.waiters[c.ID]
	h.mu.Unlock()

	if !exists {
		return fmt.Errorf("no active generation request for ID %s", c.ID)
	}

	select {
	case ch <- c:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("channel buffer full for ID %s", c.ID)
	}
}

func (h *Hub) pruneRecentLocked(now time.Time) {
	cutoff := now.Add(-1 * time.Hour)
	keep := 0
	for _, ts := range h.recentDispatches {
		if ts.After(cutoff) {
			h.recentDispatches[keep] = ts
			keep++
		}
	}
	h.recentDispatches = h.recentDispatches[:keep]
}

// PacingStats returns the number of bridge prompts dispatched in the last 10 minutes
// and the configured 10-minute rolling ceiling (0 if disabled).
func (h *Hub) PacingStats() (recent10m int, maxPer10m int) {
	snap := h.DetailedPacingStats()
	return snap.Recent10m, snap.MaxPer10m
}

// DetailedPacingStats returns full live pacing counters, hourly cap, cooldown, and pause state.
func (h *Hub) DetailedPacingStats() PacingSnapshot {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneRecentLocked(now)
	tenMinAgo := now.Add(-10 * time.Minute)
	recent10m := 0
	for _, ts := range h.recentDispatches {
		if ts.After(tenMinAgo) {
			recent10m++
		}
	}
	cooldownSec := 0
	if now.Before(h.cooldownUntil) {
		cooldownSec = int(time.Until(h.cooldownUntil).Round(time.Second).Seconds())
		if cooldownSec < 1 {
			cooldownSec = 1
		}
	}
	return PacingSnapshot{
		Recent10m:   recent10m,
		MaxPer10m:   h.maxPer10Min,
		Recent1h:    len(h.recentDispatches),
		MaxPerHour:  h.maxPerHour,
		CooldownSec: cooldownSec,
		Paused:      h.paused,
	}
}

// ComputePacingDelay calculates the adaptive anti-ban delay (base + progressive burst
// scaling + human-like jitter + rolling 10-minute/1-hour window pacing) or returns *RateLimitError
// if the circuit breaker is active.
func (h *Hub) ComputePacingDelay() (time.Duration, error) {
	return h.ComputePacingDelayWithPenalty(false, false)
}

// ComputePacingDelayForOptions calculates the adaptive anti-ban delay, adding a small
// extra breather when the turn also performs browser file uploads so upload + submit
// bursts stay well under Google's anti-abuse frequency thresholds.
func (h *Hub) ComputePacingDelayForOptions(hasAttachments bool) (time.Duration, error) {
	return h.ComputePacingDelayWithPenalty(hasAttachments, false)
}

// ComputePacingDelayWithPenalty calculates anti-ban pacing including upload breathers,
// runaway agent-loop penalty spacing, and both rolling 10-minute and 1-hour ceilings.
func (h *Hub) ComputePacingDelayWithPenalty(hasAttachments, loopPenalty bool) (time.Duration, error) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.paused {
		return 0, &PausedError{}
	}

	if now.Before(h.cooldownUntil) {
		return 0, &RateLimitError{
			RetryAfter: time.Until(h.cooldownUntil),
			Cause:      h.lastRateLimitCause,
		}
	}

	h.pruneRecentLocked(now)

	recent5m := 0
	recent10m := 0
	var oldest10m time.Time
	fiveMinAgo := now.Add(-5 * time.Minute)
	tenMinAgo := now.Add(-10 * time.Minute)
	for _, ts := range h.recentDispatches {
		if ts.After(tenMinAgo) {
			if recent10m == 0 {
				oldest10m = ts
			}
			recent10m++
		}
		if ts.After(fiveMinAgo) {
			recent5m++
		}
	}
	recent1h := len(h.recentDispatches)

	var waitDur time.Duration
	if h.minInterval > 0 && !h.lastCompletedAt.IsZero() {
		// Progressive burst backoff during long coding loops (5m short-burst + 1h sustained-session guard)
		target := h.minInterval
		jitterCap := h.jitterMax
		switch {
		case recent5m >= 10 || recent1h >= 50:
			target = h.minInterval * 4
			jitterCap = h.jitterMax * 2
		case recent5m >= 5 || recent1h >= 30:
			target = h.minInterval * 2
		}

		// File uploads issue extra HTTP upload calls to Google before prompt submission;
		// add a gentle breather so upload+submit cycles look natural.
		if hasAttachments {
			extra := h.minInterval / 2
			if extra > 1500*time.Millisecond {
				extra = 1500 * time.Millisecond
			}
			target += extra
		}

		// Slow down runaway agent retry loops so repetitive tool failures cannot spam Google.
		if loopPenalty {
			target += h.minInterval * 2
		}

		if jitterCap > 0 {
			target += time.Duration(rand.Int64N(int64(jitterCap) + 1))
		}

		elapsed := now.Sub(h.lastCompletedAt)
		if elapsed < target {
			waitDur = target - elapsed
		}
	}

	// Smooth rolling 10-minute ceiling so heavy coding sessions pace naturally
	// rather than triggering Google's hard frequency ban.
	if h.maxPer10Min > 0 && recent10m >= h.maxPer10Min && !oldest10m.IsZero() {
		oldestExpiry := oldest10m.Add(10 * time.Minute)
		if oldestExpiry.After(now) {
			windowWait := oldestExpiry.Sub(now)
			if windowWait > waitDur {
				waitDur = windowWait
			}
		}
	}

	// Rolling 1-hour ceiling for sustained multi-hour sessions.
	if h.maxPerHour > 0 && recent1h >= h.maxPerHour && len(h.recentDispatches) > 0 {
		oldestHourExpiry := h.recentDispatches[0].Add(1 * time.Hour)
		if oldestHourExpiry.After(now) {
			hourWait := oldestHourExpiry.Sub(now)
			if hourWait > waitDur {
				waitDur = hourWait
			}
		}
	}

	return waitDur, nil
}

// GenerateStream queues a prompt to the browser extension and yields text chunks as they arrive.
func (h *Hub) GenerateStream(ctx context.Context, prompt, model string, emit func(string) error) error {
	return h.GenerateStreamWithOptions(ctx, GenerateOptions{
		Prompt: prompt,
		Model:  model,
	}, emit)
}

// GenerateStreamWithOptions queues a prompt (with optional direct file attachments) to the
// browser extension, serializing concurrent dispatches and applying adaptive anti-ban
// pacing plus 429 circuit-breaker protection.
func (h *Hub) GenerateStreamWithOptions(ctx context.Context, opts GenerateOptions, emit func(string) error) error {
	if !h.IsConnected() {
		return errors.New("no browser tab connected to the bridge")
	}

	// Fail fast if paused by user or circuit breaker is already tripped.
	if h.IsPaused() {
		return &PausedError{}
	}
	if rem := h.CooldownRemaining(); rem > 0 {
		h.mu.Lock()
		cause := h.lastRateLimitCause
		h.mu.Unlock()
		return &RateLimitError{RetryAfter: rem, Cause: cause}
	}

	// Serialize browser-tab dispatches so concurrent requests (e.g. parallel agent/client
	// turns) never bypass anti-ban cooldown and fire back-to-back with 0ms spacing.
	if h.dispatchGate != nil {
		select {
		case h.dispatchGate <- struct{}{}:
			defer func() { <-h.dispatchGate }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	waitDur, rlErr := h.ComputePacingDelayWithPenalty(len(opts.Attachments) > 0, opts.LoopPenalty)
	if rlErr != nil {
		return rlErr
	}
	if waitDur > 0 {
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		// Re-check pause & circuit breaker after pacing sleep
		if h.IsPaused() {
			return &PausedError{}
		}
		if rem := h.CooldownRemaining(); rem > 0 {
			return &RateLimitError{RetryAfter: rem}
		}
	}

	reqID := fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), h.seq.Add(1))
	req := &Request{
		ID:          reqID,
		Prompt:      opts.Prompt,
		Model:       opts.Model,
		Attachments: opts.Attachments,
		NewChat:     opts.NewChat,
		CreatedAt:   time.Now(),
	}

	ch := make(chan Chunk, 512)

	h.mu.Lock()
	h.waiters[reqID] = ch
	h.pending = append(h.pending, req)
	h.recentDispatches = append(h.recentDispatches, time.Now())
	h.pruneRecentLocked(time.Now())
	h.savePacingStateLocked()
	noContentTimeout := h.noContentTimeout
	select {
	case h.pollNotify <- struct{}{}:
	default:
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.waiters, reqID)
		delete(h.inFlight, reqID)
		for i, r := range h.pending {
			if r.ID == reqID {
				h.pending = append(h.pending[:i], h.pending[i+1:]...)
				break
			}
		}
		h.lastCompletedAt = time.Now()
		h.mu.Unlock()
	}()

	const idleTimeout = 240 * time.Second
	// Absolute ceiling for any bridge turn, even one whose tab keeps sending
	// keepalive chunks — past this the turn is declared stuck and failed.
	const absoluteMaxTurn = 10 * time.Minute
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	// No-content watchdog: fires when the turn has produced nothing but empty
	// keepalive chunks for too long — the classic signature of a tab frozen by
	// Chrome mid-upload (throttled timers squeak a keepalive ~1/min, resetting
	// the idle timer forever) or one that went fully silent. "generating" phase
	// reports from the content script are real progress and reset this watchdog.
	var noContentC <-chan time.Time
	var noContentTimer *time.Timer
	if noContentTimeout > 0 {
		noContentTimer = time.NewTimer(noContentTimeout)
		noContentC = noContentTimer.C
		defer noContentTimer.Stop()
	}

	turnStartedAt := time.Now()
	lastPhase := ""

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-ch:
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)
			if !ok {
				return nil
			}
			if chunk.RateLimited {
				rem := h.CooldownRemaining()
				if rem <= 0 {
					rem = 60 * time.Second
				}
				return &RateLimitError{RetryAfter: rem, Cause: chunk.Error}
			}
			if chunk.Error != "" {
				return errors.New(chunk.Error)
			}
			if chunk.Status != "" {
				lastPhase = chunk.Status
			}
			if chunk.Text != "" {
				if noContentTimer != nil {
					noContentTimer.Stop()
					noContentC = nil
				}
				if err := emit(chunk.Text); err != nil {
					return err
				}
			}
			if chunk.Done {
				return nil
			}
			if chunk.Text == "" && chunk.Status == "generating" && noContentTimer != nil {
				// Gemini is thinking/streaming — genuine progress, not a stuck tab.
				noContentTimer.Reset(noContentTimeout)
			}
			if time.Since(turnStartedAt) > absoluteMaxTurn {
				return errors.New("browser bridge turn exceeded the 10-minute absolute limit without completing — the tab is stuck; check the Gemini tab and retry")
			}
		case <-noContentC:
			phase := lastPhase
			if phase == "" {
				phase = "unknown"
			}
			return fmt.Errorf("browser bridge produced no content within %ds (tab likely frozen or stuck in phase %q — e.g. attachment upload stalled); the server will retry via direct cookie mode when available", int(noContentTimeout.Seconds()), phase)
		case <-idleTimer.C:
			return errors.New("browser bridge generation timed out (240s with no data from the browser tab — the Gemini tab may be laggy, throttled in the background, or stuck uploading; check the tab and retry)")
		}
	}
}

// Generate collects all stream chunks and returns the complete text.
func (h *Hub) Generate(ctx context.Context, prompt, model string) (string, error) {
	return h.GenerateWithOptions(ctx, GenerateOptions{
		Prompt: prompt,
		Model:  model,
	})
}

// GenerateWithOptions collects all stream chunks for opts and returns the complete text.
func (h *Hub) GenerateWithOptions(ctx context.Context, opts GenerateOptions) (string, error) {
	var sb strings.Builder
	err := h.GenerateStreamWithOptions(ctx, opts, func(chunk string) error {
		sb.WriteString(chunk)
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}
