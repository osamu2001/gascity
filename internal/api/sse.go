package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

const sseKeepalive = 15 * time.Second

// writeSSE writes a single SSE event to w and flushes.
func writeSSE(w http.ResponseWriter, eventType string, id uint64, data []byte) {
	fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", eventType, id, data) //nolint:errcheck
	// Use ResponseController to flush through wrapped writers (e.g., logging middleware).
	if err := http.NewResponseController(w).Flush(); err != nil {
		// Flushing not supported; best-effort.
		_ = err
	}
}

// writeSSEComment writes a keepalive comment line and flushes.
func writeSSEComment(w http.ResponseWriter) {
	fmt.Fprintf(w, ": keepalive\n\n") //nolint:errcheck
	if err := http.NewResponseController(w).Flush(); err != nil {
		_ = err
	}
}

// streamEventsWithWatcher runs the SSE event loop with a pre-created watcher.
// Blocks until ctx is canceled. The watcher is closed when done.
func streamEventsWithWatcher(ctx context.Context, w http.ResponseWriter, watcher events.Watcher) {
	defer watcher.Close() //nolint:errcheck

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()

	// Channel for events from watcher.
	type result struct {
		event events.Event
		err   error
	}
	ch := make(chan result, 1)

	// readNext spawns a goroutine that reads from the watcher and sends
	// to ch, but only if ctx is still active. This prevents goroutine
	// leaks when the client disconnects. On context cancellation, any
	// goroutine blocked on watcher.Next() is unblocked by the deferred
	// watcher.Close() call, which closes the watcher's done channel.
	readNext := func() {
		go func() {
			e, err := watcher.Next()
			select {
			case ch <- result{e, err}:
			case <-ctx.Done():
				// Client disconnected; drop the result and exit.
			}
		}()
	}

	// Start first read.
	readNext()

	for {
		select {
		case <-ctx.Done():
			return
		case r := <-ch:
			if r.err != nil {
				return
			}
			data, err := json.Marshal(r.event)
			if err != nil {
				readNext()
				continue
			}
			writeSSE(w, r.event.Type, r.event.Seq, data)
			readNext()
		case <-keepalive.C:
			writeSSEComment(w)
		}
	}
}

// parseAfterSeq reads the reconnect position from Last-Event-ID or ?after_seq.
func parseAfterSeq(r *http.Request) uint64 {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	if v := r.URL.Query().Get("after_seq"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}
