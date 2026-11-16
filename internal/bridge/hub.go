package bridge

import (
	"context"
	"errors"
	"fmt"
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
	CreatedAt   time.Time    `json:"created_at"`
}

type GenerateOptions struct {
	Prompt      string
	Model       string
	Attachments []Attachment
}

type Chunk struct {
	ID    string `json:"id"`
	Text  string `json:"text,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

type inFlightEntry struct {
	req        *Request
	tabName    string
	clientID   string
	assignedAt time.Time
	hasChunks  bool
}

type Hub struct {
	mu                   sync.Mutex
	seq                  atomic.Uint64
	tabName              string
	lastHeartbeat        time.Time
	clientHeartbeats     map[string]time.Time
	pending              []*Request
	inFlight             map[string]*inFlightEntry
	waiters              map[string]chan Chunk
	pollNotify           chan struct{}
	minInterval          time.Duration
	lastCompletedAt      time.Time
	activeClientID       string
	activeConvGen        string
	sessionFingerprints  []string
	sessionUploadedFiles map[string]bool
}

func NewHub() *Hub {
	return &Hub{
		clientHeartbeats:     make(map[string]time.Time),
		inFlight:             make(map[string]*inFlightEntry),
		waiters:              make(map[string]chan Chunk),
		pollNotify:           make(chan struct{}, 1),
		sessionUploadedFiles: make(map[string]bool),
	}
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

// IsConnected returns true if a browser tab sent a poll/heartbeat within the last 45 seconds,
// or if an active generation is still in flight within the 180s window.
func (h *Hub) IsConnected() bool {
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
// the browser tab reloads (new clientID) or starts a fresh chat (new convGen).
func (h *Hub) UpdateTabSession(tabName, clientID, convGen string) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if tabName != "" {
		h.tabName = tabName
	}
	if clientID != "" {
		h.clientHeartbeats[clientID] = now
		if h.activeClientID != "" && h.activeClientID != clientID {
			h.sessionFingerprints = nil
			h.sessionUploadedFiles = make(map[string]bool)
		}
		h.activeClientID = clientID
	}
	if convGen != "" {
		if h.activeConvGen != "" && h.activeConvGen != convGen {
			h.sessionFingerprints = nil
			h.sessionUploadedFiles = make(map[string]bool)
		}
		h.activeConvGen = convGen
	}
	h.lastHeartbeat = now
}

// ActiveSession snapshots the active tab identity and previously uploaded file paths.
func (h *Hub) ActiveSession() (clientID, convGen string, uploaded map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make(map[string]bool, len(h.sessionUploadedFiles))
	for k, v := range h.sessionUploadedFiles {
		cp[k] = v
	}
	return h.activeClientID, h.activeConvGen, cp
}

// MatchDeltaPrefix returns the number of leading messages already present in the
// active browser tab's live conversation if fingerprints strictly extend it.
func (h *Hub) MatchDeltaPrefix(fingerprints []string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev := h.sessionFingerprints
	if len(prev) == 0 || len(fingerprints) <= len(prev) {
		return 0
	}
	for i := range prev {
		if prev[i] != fingerprints[i] {
			return 0
		}
	}
	return len(prev)
}

// RecordTurnSuccess saves the conversation prefix and uploaded file paths for the
// active tab session so subsequent follow-up turns can send compact deltas and avoid
// re-uploading files already present in the open chat.
func (h *Hub) RecordTurnSuccess(clientID, convGen string, fingerprints []string, uploadedPaths []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if clientID != h.activeClientID || convGen != h.activeConvGen {
		return
	}
	cp := make([]string, len(fingerprints))
	copy(cp, fingerprints)
	h.sessionFingerprints = cp
	for _, p := range uploadedPaths {
		if p != "" {
			h.sessionUploadedFiles[p] = true
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

	h.mu.Lock()
	if len(h.pending) > 0 {
		req := h.pending[0]
		h.pending = h.pending[1:]
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
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.pending) > 0 {
			req := h.pending[0]
			h.pending = h.pending[1:]
			h.inFlight[req.ID] = &inFlightEntry{
				req:        req,
				tabName:    tabName,
				clientID:   clientID,
				assignedAt: time.Now(),
			}
			return req, nil
		}
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, nil
	}
}

// SubmitChunk routes an incoming chunk from the browser extension to the waiting HTTP request.
func (h *Hub) SubmitChunk(c Chunk) error {
	h.Touch("")

	h.mu.Lock()
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

func (h *Hub) remainingCooldown() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.minInterval <= 0 || h.lastCompletedAt.IsZero() {
		return 0
	}
	elapsed := time.Since(h.lastCompletedAt)
	if elapsed >= h.minInterval {
		return 0
	}
	return h.minInterval - elapsed
}

// GenerateStream queues a prompt to the browser extension and yields text chunks as they arrive.
func (h *Hub) GenerateStream(ctx context.Context, prompt, model string, emit func(string) error) error {
	return h.GenerateStreamWithOptions(ctx, GenerateOptions{
		Prompt: prompt,
		Model:  model,
	}, emit)
}

// GenerateStreamWithOptions queues a prompt (with optional direct file attachments) to the
// browser extension, applying anti-ban cooldown pacing between consecutive requests.
func (h *Hub) GenerateStreamWithOptions(ctx context.Context, opts GenerateOptions, emit func(string) error) error {
	if !h.IsConnected() {
		return errors.New("no browser tab connected to the bridge")
	}

	// Anti-ban pacing: avoid rapid-fire back-to-back automated bursts
	if waitDur := h.remainingCooldown(); waitDur > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDur):
		}
	}

	reqID := fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), h.seq.Add(1))
	req := &Request{
		ID:          reqID,
		Prompt:      opts.Prompt,
		Model:       opts.Model,
		Attachments: opts.Attachments,
		CreatedAt:   time.Now(),
	}

	ch := make(chan Chunk, 512)

	h.mu.Lock()
	h.waiters[reqID] = ch
	h.pending = append(h.pending, req)
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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				return nil
			}
			if chunk.Error != "" {
				return errors.New(chunk.Error)
			}
			if chunk.Text != "" {
				if err := emit(chunk.Text); err != nil {
					return err
				}
			}
			if chunk.Done {
				return nil
			}
		case <-time.After(180 * time.Second):
			return errors.New("browser bridge generation timed out (180s)")
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
