package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPickupTimeoutFailsUnclaimedRequest(t *testing.T) {
	h := NewHub()
	h.Heartbeat("vertexaisearch.cloud.google.com", "tab_1")
	h.SetPickupTimeout(400 * time.Millisecond)

	start := time.Now()
	err := h.GenerateStreamWithOptions(context.Background(), GenerateOptions{Prompt: "hello"}, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for unclaimed request")
	}
	var pkErr *PickupError
	if !errors.As(err, &pkErr) {
		t.Fatalf("expected typed PickupError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "not picked up") {
		t.Fatalf("expected pickup timeout error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("pickup timeout took too long: %v", elapsed)
	}
}

func TestPickupHookNotCalledOnTimeout(t *testing.T) {
	h := NewHub()
	h.Heartbeat("vertexaisearch.cloud.google.com", "tab_1")
	h.SetPickupTimeout(300 * time.Millisecond)

	called := false
	err := h.GenerateStreamWithPickupHook(context.Background(), GenerateOptions{Prompt: "hello"},
		func() error { called = true; return nil },
		func(string) error { return nil })
	var pkErr *PickupError
	if !errors.As(err, &pkErr) {
		t.Fatalf("expected typed PickupError, got %T: %v", err, err)
	}
	if called {
		t.Fatal("onPickup must not fire when no tab claims the request")
	}
}

func TestPickupHookFiresOnClaimBeforeChunks(t *testing.T) {
	h := NewHub()
	h.Heartbeat("vertexaisearch.cloud.google.com", "tab_1")
	h.SetPickupTimeout(2 * time.Second)

	hookDone := make(chan struct{})
	var sawTextBeforeHook bool
	go func() {
		time.Sleep(50 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := h.WaitForNextRequestForClient(ctx, "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		_ = h.SubmitChunk(Chunk{ID: req.ID, Text: "hi", Done: true})
	}()

	err := h.GenerateStreamWithPickupHook(context.Background(), GenerateOptions{Prompt: "hello"},
		func() error {
			time.Sleep(50 * time.Millisecond) // hold the hook open; emits must wait
			close(hookDone)
			return nil
		},
		func(string) error {
			select {
			case <-hookDone:
			default:
				sawTextBeforeHook = true
			}
			return nil
		})
	if err != nil {
		t.Fatalf("claimed request must succeed: %v", err)
	}
	select {
	case <-hookDone:
	default:
		t.Fatal("onPickup must fire when a tab claims the request")
	}
	if sawTextBeforeHook {
		t.Fatal("emitted chunks must wait for onPickup to finish")
	}
}

func TestPickupTimerIgnoredOnceClaimed(t *testing.T) {
	h := NewHub()
	h.Heartbeat("vertexaisearch.cloud.google.com", "tab_1")
	h.SetPickupTimeout(300 * time.Millisecond)

	// Poller claims quickly (before the pickup timer fires), reports progress,
	// then finishes only AFTER the timer has fired — the claim check must disarm
	// the pickup timer and let the slow turn complete normally.
	go func() {
		time.Sleep(50 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := h.WaitForNextRequestForClient(ctx, "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		_ = h.SubmitChunk(Chunk{ID: req.ID, Status: "generating"})
		time.Sleep(500 * time.Millisecond) // past the 300ms pickup timer
		_ = h.SubmitChunk(Chunk{ID: req.ID, Text: "hi", Done: true})
	}()

	if err := h.GenerateStreamWithOptions(context.Background(), GenerateOptions{Prompt: "hello"}, func(string) error { return nil }); err != nil {
		t.Fatalf("claimed request must not fail on pickup timeout: %v", err)
	}
}
