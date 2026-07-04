package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetryBackoff shrinks retryBackoff for tests so they don't spend
// real seconds sleeping between attempts — restored after each test.
func fastRetryBackoff(t *testing.T) {
	t.Helper()
	orig := retryBackoffFunc
	retryBackoffFunc = func(failedAttempts int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { retryBackoffFunc = orig })
}

func TestDoWithRetrySucceedsAfterTransient5xx(t *testing.T) {
	fastRetryBackoff(t)

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")
	c := New("minimax-m3")

	ch, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	for range ch {
	}

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server received %d requests, want exactly 3 (2 failures + 1 success)", got)
	}
}

func TestDoWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	fastRetryBackoff(t)

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")
	c := New("minimax-m3")

	_, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error after exhausting all retry attempts")
	}
	if got := atomic.LoadInt32(&calls); got != maxSendAttempts {
		t.Errorf("server received %d requests, want exactly maxSendAttempts (%d)", got, maxSendAttempts)
	}
}

func TestDoWithRetryDoesNotRetryClientErrors(t *testing.T) {
	fastRetryBackoff(t)

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")
	c := New("minimax-m3")

	_, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error for a 401")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server received %d requests, want exactly 1 — a 401 should not be retried", got)
	}
}

func TestDoWithRetryRespectsContextCancellationDuringBackoff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")
	c := New("minimax-m3")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	_, err := c.SendStreaming(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s to fail on a cancelled context, want near-instant", elapsed)
	}
}
