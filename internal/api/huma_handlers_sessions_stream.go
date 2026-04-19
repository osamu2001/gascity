package api

import (
	"context"
	"encoding/json"
	"log"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/sessionlog"
)

// SSE stream handlers for the session endpoint. resolveSessionStream picks
// the right transcript format and source; streamSession drives the actual
// per-request streaming loop.

func (s *Server) resolveSessionStream(input *SessionStreamInput) (*sessionStreamState, error) {
	store := s.state.CityBeadStore()
	if store == nil {
		return nil, huma.Error503ServiceUnavailable("no bead store configured")
	}

	id, err := s.resolveSessionIDAllowClosedWithConfig(store, input.ID)
	if err != nil {
		return nil, humaResolveError(err)
	}

	mgr := s.sessionManager(store)
	info, err := mgr.Get(id)
	if err != nil {
		return nil, humaSessionManagerError(err)
	}
	path, err := mgr.TranscriptPath(id, s.sessionLogPaths())
	if err != nil {
		return nil, humaSessionManagerError(err)
	}

	sp := s.state.SessionProvider()
	running := info.State == session.StateActive && sp.IsRunning(info.SessionName)
	if path == "" && !running {
		return nil, huma.Error404NotFound("session " + id + " has no live output")
	}

	return &sessionStreamState{info: info, path: path, running: running}, nil
}

// checkSessionStream is the precheck for GET /v0/session/{id}/stream.

func (s *Server) checkSessionStream(_ context.Context, input *SessionStreamInput) error {
	_, err := s.resolveSessionStream(input)
	return err
}

// streamSession is the SSE streaming callback for GET /v0/session/{id}/stream.

func (s *Server) streamSession(hctx huma.Context, input *SessionStreamInput, send sse.Sender) {
	state, err := s.resolveSessionStream(input)
	if err != nil {
		// Invariant violation: precheck passed, body resolve failed.
		// Session vanished between precheck and streaming start, or a
		// race we didn't anticipate. Headers are already committed so
		// we can't return an HTTP error — log so the next debugger has
		// a starting point instead of a mute disconnect.
		log.Printf("api: session-stream: resolve failed after precheck city=%s id=%s: %v",
			input.CityName, input.ID, err)
		return
	}
	info := state.info
	path := state.path
	running := state.running
	format := input.Format

	// Custom session state headers.
	if info.State != "" {
		hctx.SetHeader("GC-Session-State", string(info.State))
	}
	if !running {
		hctx.SetHeader("GC-Session-Status", "stopped")
	}

	reqCtx := hctx.Context()
	if info.Closed {
		if format == "raw" {
			s.emitClosedSessionSnapshotRawHuma(send, info, path)
		} else {
			s.emitClosedSessionSnapshotHuma(send, info, path)
		}
		return
	}
	switch {
	case path != "":
		if format == "raw" {
			s.streamSessionTranscriptLogRaw(reqCtx, send, info, path)
		} else {
			s.streamSessionTranscriptLog(reqCtx, send, info, path)
		}
	case format == "raw":
		if running {
			s.streamSessionPeekRaw(reqCtx, send, info)
		} else {
			_ = send(sse.Message{ID: 1, Data: SessionStreamRawMessageEvent{
				ID:       info.ID,
				Template: info.Template,
				Provider: info.Provider,
				Format:   "raw",
				Messages: []SessionRawMessageFrame{},
			}})
		}
	default:
		s.streamSessionPeek(reqCtx, send, info)
	}
}

func (s *Server) emitClosedSessionSnapshotHuma(send sse.Sender, info session.Info, logPath string) {
	if logPath == "" {
		return
	}
	sess, err := sessionlog.ReadProviderFile(info.Provider, logPath, 0)
	if err != nil {
		return
	}

	turns := make([]outputTurn, 0, len(sess.Messages))
	for _, entry := range sess.Messages {
		turn := entryToTurn(entry)
		if turn.Text == "" {
			continue
		}
		turns = append(turns, turn)
	}
	if len(turns) == 0 {
		return
	}

	_ = send(sse.Message{ID: 1, Data: SessionStreamMessageEvent{
		ID:       info.ID,
		Template: info.Template,
		Provider: info.Provider,
		Format:   "conversation",
		Turns:    turns,
	}})
	_ = send(sse.Message{ID: 2, Data: SessionActivityEvent{Activity: "idle"}})
}

func (s *Server) emitClosedSessionSnapshotRawHuma(send sse.Sender, info session.Info, logPath string) {
	if logPath == "" {
		return
	}
	sess, err := sessionlog.ReadProviderFileRaw(info.Provider, logPath, 0)
	if err != nil {
		return
	}

	rawMessages := make([]json.RawMessage, 0, len(sess.Messages))
	for _, entry := range sess.Messages {
		if len(entry.Raw) == 0 {
			continue
		}
		rawMessages = append(rawMessages, entry.Raw)
	}
	if len(rawMessages) == 0 {
		return
	}

	_ = send(sse.Message{ID: 1, Data: SessionStreamRawMessageEvent{
		ID:       info.ID,
		Template: info.Template,
		Provider: info.Provider,
		Format:   "raw",
		Messages: wrapRawFrameBytes(rawMessages),
	}})
	_ = send(sse.Message{ID: 2, Data: SessionActivityEvent{Activity: "idle"}})
}
