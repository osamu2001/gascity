package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

type peekOnlyHandle struct {
	output string
}

func (h peekOnlyHandle) Peek(context.Context, int) (string, error) {
	return h.output, nil
}

func TestStreamSessionPeekAcceptsPeekCapability(t *testing.T) {
	srv := New(newSessionFakeState(t))
	info := session.Info{ID: "sess-1", Template: "probe"}
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.streamSessionPeek(ctx, rec, info, peekOnlyHandle{output: "hello from peek"})
		close(done)
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), "hello from peek") {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("stream body missing peek output: %s", rec.Body.String())
}

var _ worker.PeekHandle = peekOnlyHandle{}
