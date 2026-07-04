package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientHasNoOverallTimeout(t *testing.T) {
	c := New("minimax-m3")
	if c.http.Timeout != 0 {
		t.Errorf("http.Client.Timeout = %s, want 0 (unset) — that field bounds the entire request including reading a streaming body, not just connection setup", c.http.Timeout)
	}
}

func TestTransportHasResponseHeaderTimeout(t *testing.T) {
	transport := newTransport()
	if transport.ResponseHeaderTimeout <= 0 {
		t.Error("ResponseHeaderTimeout is unset — a connection that never responds at all would hang forever")
	}
}

// TestResponseHeaderTimeoutActuallyFires exercises the real behavior: a
// server that never sends a response should time out, proving
// ResponseHeaderTimeout is wired into an actual request, not just set on
// an unused Transport value.
func TestResponseHeaderTimeoutActuallyFires(t *testing.T) {
	blockForever := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockForever // never respond
	}))
	// Close the channel (unblocking the handler) before server.Close(),
	// which otherwise waits for that in-flight handler to return —
	// defers run LIFO, so server.Close() must be deferred first.
	defer server.Close()
	defer close(blockForever)

	transport := newTransport()
	transport.ResponseHeaderTimeout = 200 * time.Millisecond
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected a timeout error from a server that never responds")
	}
}
