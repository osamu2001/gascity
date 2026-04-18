package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/sessionlog"
	workdirutil "github.com/gastownhall/gascity/internal/workdir"
	"github.com/gastownhall/gascity/internal/worker"
)

var errSessionTemplateNotFound = errors.New("session template not found")

type sessionPendingResponse struct {
	Supported bool                        `json:"supported"`
	Pending   *runtime.PendingInteraction `json:"pending,omitempty"`
}

// SessionStreamMessageEvent carries normalized conversation turns on the
// session SSE stream.
type SessionStreamMessageEvent struct {
	ID         string                     `json:"id"`
	Template   string                     `json:"template"`
	Provider   string                     `json:"provider" doc:"Producing provider identifier (claude, codex, gemini, open-code, etc.)."`
	Format     string                     `json:"format"`
	Turns      []outputTurn               `json:"turns"`
	Pagination *sessionlog.PaginationInfo `json:"pagination,omitempty"`
}

// SessionStreamRawMessageEvent carries provider-native transcript frames on
// the session SSE stream.
type SessionStreamRawMessageEvent struct {
	ID         string                     `json:"id"`
	Template   string                     `json:"template"`
	Provider   string                     `json:"provider" doc:"Producing provider identifier (claude, codex, gemini, open-code, etc.). Consumers use this to dispatch per-provider frame parsing."`
	Format     string                     `json:"format"`
	Messages   []SessionRawMessageFrame   `json:"messages" doc:"Provider-native transcript frames, emitted verbatim as the provider wrote them."`
	Pagination *sessionlog.PaginationInfo `json:"pagination,omitempty"`
}

func (s *Server) sessionLogPaths() []string {
	if s.sessionLogSearchPaths != nil {
		return s.sessionLogSearchPaths
	}
	cfg := s.state.Config()
	if cfg == nil {
		return sessionlog.DefaultSearchPaths()
	}
	return sessionlog.MergeSearchPaths(cfg.Daemon.ObservePaths)
}

func sessionCreateHints(resolved *config.ResolvedProvider) runtime.Config {
	return runtime.Config{
		ReadyPromptPrefix:      resolved.ReadyPromptPrefix,
		ReadyDelayMs:           resolved.ReadyDelayMs,
		ProcessNames:           resolved.ProcessNames,
		EmitsPermissionWarning: resolved.EmitsPermissionWarning,
	}
}

func sessionResumeHints(resolved *config.ResolvedProvider, workDir string) runtime.Config {
	return runtime.Config{
		WorkDir:                workDir,
		ReadyPromptPrefix:      resolved.ReadyPromptPrefix,
		ReadyDelayMs:           resolved.ReadyDelayMs,
		ProcessNames:           resolved.ProcessNames,
		EmitsPermissionWarning: resolved.EmitsPermissionWarning,
		Env:                    resolved.Env,
	}
}

func sessionExplicitNameForCreate(agentCfg config.Agent, alias string) (string, error) {
	if !agentCfg.SupportsMultipleSessions() || strings.TrimSpace(alias) != "" {
		return "", nil
	}
	return session.GenerateAdhocExplicitName(agentCfg.Name)
}

func (s *Server) resolveSessionWorkDir(agentCfg config.Agent, qualifiedName string) (string, error) {
	cfg := s.state.Config()
	if cfg == nil {
		return "", errors.New("no city config loaded")
	}
	workDir, err := workdirutil.ResolveWorkDirPathStrict(
		s.state.CityPath(),
		workdirutil.CityName(s.state.CityPath(), cfg),
		qualifiedName,
		agentCfg,
		cfg.Rigs,
	)
	if err != nil {
		return "", err
	}
	if workDir == "" {
		workDir = s.state.CityPath()
	}
	return workDir, nil
}

// resolveSessionTemplateWithBareNameFallback resolves a session template
// by name, retrying with the qualified name when the input is a bare
// agent name that matches exactly one configured agent. Keeps the
// two-phase lookup out of the handler.
func (s *Server) resolveSessionTemplateWithBareNameFallback(name string) (*config.ResolvedProvider, string, string, string, error) {
	resolved, workDir, transport, template, err := s.resolveSessionTemplate(name)
	if err == nil {
		return resolved, workDir, transport, template, nil
	}
	if !errors.Is(err, errSessionTemplateNotFound) || strings.Contains(name, "/") {
		return nil, "", "", "", err
	}
	agentCfg, ok := findUniqueAgentTemplateByBareName(s.state.Config(), name)
	if !ok {
		return nil, "", "", "", err
	}
	return s.resolveSessionTemplate(agentCfg.QualifiedName())
}

func (s *Server) resolveSessionTemplate(template string) (*config.ResolvedProvider, string, string, string, error) {
	cfg := s.state.Config()
	if cfg == nil {
		return nil, "", "", "", errors.New("no city config loaded")
	}
	agentCfg, ok := resolveSessionTemplateAgent(cfg, template)
	if !ok {
		return nil, "", "", "", errSessionTemplateNotFound
	}
	resolved, err := config.ResolveProvider(&agentCfg, &cfg.Workspace, cfg.Providers, exec.LookPath)
	if err != nil {
		return nil, "", "", "", err
	}
	workDir, err := s.resolveSessionWorkDir(agentCfg, agentCfg.QualifiedName())
	if err != nil {
		return nil, "", "", "", err
	}
	return resolved, workDir, agentCfg.Session, agentCfg.QualifiedName(), nil
}

func (s *Server) buildSessionResume(info session.Info) (string, runtime.Config) {
	cmd := session.BuildResumeCommand(info)
	resolved, workDir := s.resolveSessionRuntime(info)
	if resolved == nil {
		return cmd, runtime.Config{WorkDir: info.WorkDir}
	}
	resolvedInfo := info
	resolvedInfo.Command = resolved.CommandString()
	resolvedInfo.Provider = resolved.Name
	resolvedInfo.ResumeFlag = resolved.ResumeFlag
	resolvedInfo.ResumeStyle = resolved.ResumeStyle
	resolvedInfo.ResumeCommand = resolved.ResumeCommand
	return session.BuildResumeCommand(resolvedInfo), sessionResumeHints(resolved, workDir)
}

func (s *Server) resolveSessionRuntime(info session.Info) (*config.ResolvedProvider, string) {
	kind := s.sessionKind(info.ID)
	if kind != "provider" {
		resolved, workDir, _, _, err := s.resolveSessionTemplate(info.Template)
		if err == nil {
			if info.WorkDir != "" {
				workDir = info.WorkDir
			}
			return resolved, workDir
		}
	}

	resolved, err := s.resolveBareProvider(info.Template)
	if err != nil {
		return nil, ""
	}
	workDir := info.WorkDir
	if workDir == "" {
		workDir = s.state.CityPath()
	}
	return resolved, workDir
}

// sessionKind reads the persisted mc_session_kind from bead metadata.
func (s *Server) sessionKind(sessionID string) string {
	store := s.state.CityBeadStore()
	if store == nil {
		return ""
	}
	b, err := store.Get(sessionID)
	if err != nil {
		return ""
	}
	return b.Metadata["mc_session_kind"]
}

// resolveBareProvider resolves a provider by name without an agent template.
func (s *Server) resolveBareProvider(providerName string) (*config.ResolvedProvider, error) {
	cfg := s.state.Config()
	if cfg == nil {
		return nil, errors.New("no city config loaded")
	}
	return config.ResolveProvider(
		&config.Agent{Provider: providerName},
		&cfg.Workspace,
		cfg.Providers,
		exec.LookPath,
	)
}

func (s *Server) persistSessionMeta(store beads.Store, sessionID, kind, projectID string, optMeta map[string]string) {
	batch := make(map[string]string)
	for k, v := range optMeta {
		batch[k] = v
	}
	if kind != "" && kind != "provider" {
		batch["mc_session_kind"] = kind
	}
	if projectID != "" {
		batch["mc_project_id"] = projectID
	}
	if len(batch) > 0 {
		if err := store.SetMetadataBatch(sessionID, batch); err != nil {
			log.Printf("persistSessionMeta: session %s: %v", sessionID, err)
		}
	}
}

func (s *Server) handleSessionTranscript(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	id, err := s.resolveSessionIDAllowClosedWithConfig(store, r.PathValue("id"))
	if err != nil {
		writeResolveError(w, err)
		return
	}

	mgr := s.sessionManager(store)
	info, err := mgr.Get(id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}

	path, err := mgr.TranscriptPath(id, s.sessionLogPaths())
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}

	wantRaw := r.URL.Query().Get("format") == "raw"

	if path != "" {
		tail := 0
		if v := r.URL.Query().Get("tail"); v != "" {
			if n, convErr := strconv.Atoi(v); convErr == nil && n >= 0 {
				tail = n
			}
		}
		before := r.URL.Query().Get("before")

		if wantRaw {
			// Raw format uses ReadFileRaw (no display-type filtering) so
			// all entry types are returned — consistent with the raw
			// stream and snapshot paths.
			var rawSess *sessionlog.Session
			if before != "" {
				rawSess, err = sessionlog.ReadProviderFileRawOlder(info.Provider, path, tail, before)
			} else {
				rawSess, err = sessionlog.ReadProviderFileRaw(info.Provider, path, tail)
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "reading session log: "+err.Error())
				return
			}
			msgs := make([]json.RawMessage, 0, len(rawSess.Messages))
			for _, entry := range rawSess.Messages {
				if len(entry.Raw) > 0 {
					msgs = append(msgs, entry.Raw)
				}
			}
			writeJSON(w, http.StatusOK, sessionRawTranscriptResponse{
				ID:         info.ID,
				Template:   info.Template,
				Format:     "raw",
				Messages:   msgs,
				Pagination: rawSess.Pagination,
			})
			return
		}

		var sess *sessionlog.Session
		if before != "" {
			sess, err = sessionlog.ReadProviderFileOlder(info.Provider, path, tail, before)
		} else {
			sess, err = sessionlog.ReadProviderFile(info.Provider, path, tail)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "reading session log: "+err.Error())
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
		writeJSON(w, http.StatusOK, sessionTranscriptResponse{
			ID:         info.ID,
			Template:   info.Template,
			Format:     "conversation",
			Turns:      turns,
			Pagination: sess.Pagination,
		})
		return
	}

	if wantRaw {
		writeJSON(w, http.StatusOK, sessionRawTranscriptResponse{
			ID:       info.ID,
			Template: info.Template,
			Format:   "raw",
			Messages: []json.RawMessage{},
		})
		return
	}

	if info.State == session.StateActive && s.state.SessionProvider().IsRunning(info.SessionName) {
		output, peekErr := s.state.SessionProvider().Peek(info.SessionName, 100)
		if peekErr != nil {
			writeError(w, http.StatusInternalServerError, "internal", peekErr.Error())
			return
		}
		turns := []outputTurn{}
		if output != "" {
			turns = append(turns, outputTurn{Role: "output", Text: output})
		}
		writeJSON(w, http.StatusOK, sessionTranscriptResponse{
			ID:       info.ID,
			Template: info.Template,
			Format:   "text",
			Turns:    turns,
		})
		return
	}

	writeJSON(w, http.StatusOK, sessionTranscriptResponse{
		ID:       info.ID,
		Template: info.Template,
		Format:   "conversation",
		Turns:    []outputTurn{},
	})
}

func (s *Server) handleSessionSubmit(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	var body sessionSubmitRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "invalid", "message is required")
		return
	}
	if body.Intent == "" {
		body.Intent = session.SubmitIntentDefault
	}
	switch body.Intent {
	case session.SubmitIntentDefault, session.SubmitIntentFollowUp, session.SubmitIntentInterruptNow:
	default:
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("intent must be one of %q, %q, or %q", session.SubmitIntentDefault, session.SubmitIntentFollowUp, session.SubmitIntentInterruptNow))
		return
	}

	idemKey := scopedIdemKey(r, r.Header.Get("Idempotency-Key"))
	var bodyHash string
	if idemKey != "" {
		bodyHash = hashBody(body)
		if s.idem.handleIdempotent(w, idemKey, bodyHash) {
			return
		}
	}

	id, err := s.resolveSessionIDMaterializingNamedWithContext(r.Context(), store, r.PathValue("id"))
	if err != nil {
		s.idem.unreserve(idemKey)
		writeResolveError(w, err)
		return
	}

	outcome, err := s.submitMessageToSession(r.Context(), store, id, body.Message, body.Intent)
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	resp := map[string]any{
		"status": "accepted",
		"id":     id,
		"queued": outcome.Queued,
		"intent": string(body.Intent),
	}
	s.idem.storeResponse(idemKey, bodyHash, http.StatusAccepted, resp)
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleSessionMessage(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	var body sessionMessageRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "invalid", "message is required")
		return
	}

	idemKey := scopedIdemKey(r, r.Header.Get("Idempotency-Key"))
	var bodyHash string
	if idemKey != "" {
		bodyHash = hashBody(body)
		if s.idem.handleIdempotent(w, idemKey, bodyHash) {
			return
		}
	}

	id, err := s.resolveSessionIDMaterializingNamedWithContext(r.Context(), store, r.PathValue("id"))
	if err != nil {
		s.idem.unreserve(idemKey)
		writeResolveError(w, err)
		return
	}

	if err := s.sendUserMessageToSession(r.Context(), store, id, body.Message); err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	resp := map[string]string{"status": "accepted", "id": id}
	s.idem.storeResponse(idemKey, bodyHash, http.StatusAccepted, resp)
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleSessionKill(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	id, err := s.resolveSessionIDWithConfig(store, r.PathValue("id"))
	if err != nil {
		writeResolveError(w, err)
		return
	}

	mgr := s.sessionManager(store)
	if err := mgr.Kill(id); err != nil {
		writeSessionManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": id})
}

func (s *Server) handleSessionStop(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	id, err := s.resolveSessionIDWithConfig(store, r.PathValue("id"))
	if err != nil {
		writeResolveError(w, err)
		return
	}

	handle, err := s.workerHandleForSession(store, id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	if err := handle.Interrupt(r.Context(), worker.InterruptRequest{}); err != nil {
		writeSessionManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": id})
}

func (s *Server) handleSessionPending(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	id, err := s.resolveSessionIDWithConfig(store, r.PathValue("id"))
	if err != nil {
		writeResolveError(w, err)
		return
	}

	mgr := s.sessionManager(store)
	pending, supported, err := mgr.Pending(id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionPendingResponse{
		Supported: supported,
		Pending:   pending,
	})
}

func (s *Server) handleSessionRespond(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	id, err := s.resolveSessionIDWithConfig(store, r.PathValue("id"))
	if err != nil {
		writeResolveError(w, err)
		return
	}

	var body sessionRespondRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if body.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid", "action is required")
		return
	}

	idemKey := scopedIdemKey(r, r.Header.Get("Idempotency-Key"))
	var bodyHash string
	if idemKey != "" {
		bodyHash = hashBody(body)
		if s.idem.handleIdempotent(w, idemKey, bodyHash) {
			return
		}
	}

	handle, err := s.workerHandleForSession(store, id)
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}
	if err := handle.Respond(r.Context(), worker.InteractionResponse{
		RequestID: body.RequestID,
		Action:    body.Action,
		Text:      body.Text,
		Metadata:  body.Metadata,
	}); err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	resp := map[string]string{"status": "accepted", "id": id}
	s.idem.storeResponse(idemKey, bodyHash, http.StatusAccepted, resp)
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleSessionStream(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	id, err := s.resolveSessionIDAllowClosedWithConfig(store, r.PathValue("id"))
	if err != nil {
		writeResolveError(w, err)
		return
	}

	mgr := s.sessionManager(store)
	info, err := mgr.Get(id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	path, err := mgr.TranscriptPath(id, s.sessionLogPaths())
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}

	sp := s.state.SessionProvider()
	running := info.State == session.StateActive && sp.IsRunning(info.SessionName)
	if path == "" && !running {
		writeError(w, http.StatusNotFound, "not_found", "session "+id+" has no live output")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if info.State != "" {
		w.Header().Set("GC-Session-State", string(info.State))
	}
	if !running {
		w.Header().Set("GC-Session-Status", "stopped")
	}
	w.WriteHeader(http.StatusOK)
	if err := http.NewResponseController(w).Flush(); err != nil {
		_ = err
	}

	ctx := r.Context()
	format := r.URL.Query().Get("format")
	if info.Closed {
		if format == "raw" {
			s.emitClosedSessionSnapshotRaw(w, info, path)
		} else {
			s.emitClosedSessionSnapshot(w, info, path)
		}
		return
	}
	switch {
	case path != "":
		if format == "raw" {
			s.streamSessionTranscriptLogRaw(ctx, w, info, path)
		} else {
			s.streamSessionTranscriptLog(ctx, w, info, path)
		}
	case format == "raw":
		// No log file yet. If the session is running, poll tmux pane content
		// and wrap it as a fake raw JSONL assistant message so MC's existing
		// rendering pipeline shows terminal output (e.g. OAuth prompts).
		if running {
			s.streamSessionPeekRaw(ctx, w, info)
		} else {
			data, _ := json.Marshal(sessionRawTranscriptResponse{
				ID:       info.ID,
				Template: info.Template,
				Format:   "raw",
				Messages: []json.RawMessage{},
			})
			writeSSE(w, "message", 1, data)
		}
		return
	default:
		s.streamSessionPeek(ctx, w, info)
	}
}

func (s *Server) emitClosedSessionSnapshot(w http.ResponseWriter, info session.Info, logPath string) {
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

	if err := send(sse.Message{ID: 1, Data: SessionStreamMessageEvent{
		ID:       info.ID,
		Template: info.Template,
		Provider: info.Provider,
		Format:   "conversation",
		Turns:    turns,
	}}); err != nil {
		return
	}
	// Closed session is definitionally idle.
	_ = send(sse.Message{ID: 2, Data: SessionActivityEvent{Activity: "idle"}})
}

func (s *Server) emitClosedSessionSnapshotRaw(send sse.Sender, info session.Info, logPath string) {
	if logPath == "" {
		return
	}
	sess, err := sessionlog.ReadProviderFileRaw(info.Provider, logPath, 0)
	if err != nil {
		return
	}

	rawMessageBytes := sess.RawPayloadBytes()
	if len(rawMessageBytes) == 0 {
		return
	}

	// Closed-session snapshot: emit raw bytes end-to-end so int64
	// tool-call IDs and nanosecond timestamps are byte-faithful to what
	// the provider wrote. The streaming path (streamSessionTranscriptLogRaw)
	// already uses wrapRawFrameBytes; the snapshot path now matches.
	if err := send(sse.Message{ID: 1, Data: SessionStreamRawMessageEvent{
		ID:       info.ID,
		Template: info.Template,
		Provider: info.Provider,
		Format:   "raw",
		Messages: wrapRawFrameBytes(rawMessageBytes),
	}}); err != nil {
		return
	}
	_ = send(sse.Message{ID: 2, Data: SessionActivityEvent{Activity: "idle"}})
}

func (s *Server) streamSessionTranscriptLogRaw(ctx context.Context, send sse.Sender, info session.Info, logPath string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	send = cancelOnSendError(send, cancel)

	lw := newLogFileWatcher(logPath)
	defer lw.Close()

	var lastSize int64
	var lastSentUUID string
	var seq int
	var lastActivity string
	sentUUIDs := make(map[string]struct{})
	lw.onReset = func() { lastSize = 0; lastActivity = "" }

	readAndEmit := func() {
		stat, err := os.Stat(logPath)
		if err != nil {
			return
		}
		if stat.Size() == lastSize {
			return
		}

		// Use tail=1 (last compaction segment) to limit parsing scope,
		// consistent with the non-raw streaming path.
		sess, err := sessionlog.ReadProviderFileRaw(info.Provider, logPath, 1)
		if err != nil {
			return
		}
		lastSize = stat.Size()

		// Compute activity early (used after message emission).
		activity := sessionlog.InferActivityFromEntries(sess.Messages)

		// Keep raw bytes end-to-end. Previously we Unmarshaled entry.Raw
		// into `any` and remarshaled in wrapRawFrames — that round-trip
		// loses int64 precision above 2^53 (tool-call IDs, nanosecond
		// timestamps) and does not preserve map-key order. Provider-native
		// frames must ship byte-faithful; we use json.RawMessage so the
		// wire output matches what the provider wrote verbatim.
		rawBytes := make([]json.RawMessage, 0, len(sess.Messages))
		uuids := make([]string, 0, len(sess.Messages))
		for _, entry := range sess.Messages {
			if len(entry.Raw) == 0 {
				continue
			}
			// Validate that the bytes are well-formed JSON; skip malformed
			// frames the same way the previous Unmarshal branch did.
			if !json.Valid(entry.Raw) {
				continue
			}
			rawBytes = append(rawBytes, entry.Raw)
			uuids = append(uuids, entry.UUID)
		}

		// Emit messages if there are new ones.
		if len(rawBytes) > 0 {
			var toSend []json.RawMessage

			if lastSentUUID == "" {
				// First emission: send everything.
				toSend = rawBytes
			} else {
				found := false
				for i, uuid := range uuids {
					if uuid == lastSentUUID {
						toSend = rawBytes[i+1:]
						found = true
						break
					}
				}
				if !found {
					// Cursor lost (DAG rewrite, compaction). Instead of
					// re-syncing from the beginning (which causes duplicate/
					// out-of-order messages on the client), emit only messages
					// we haven't previously sent.
					log.Printf("session stream raw: cursor %s lost, emitting only new messages", lastSentUUID)
					for i, uuid := range uuids {
						if _, seen := sentUUIDs[uuid]; !seen {
							toSend = append(toSend, rawBytes[i])
						}
					}
				}
			}

			if len(toSend) > 0 {
				seq++
				_ = send(sse.Message{ID: seq, Data: SessionStreamRawMessageEvent{
					ID:       info.ID,
					Template: info.Template,
					Provider: info.Provider,
					Format:   "raw",
					Messages: wrapRawFrameBytes(toSend),
				}})
			}

			// Track all current UUIDs so cursor-lost can filter correctly.
			lastSentUUID = uuids[len(uuids)-1]
			for _, uuid := range uuids {
				sentUUIDs[uuid] = struct{}{}
			}
		}

		// Emit activity after content so clients receive data before state change.
		if activity != "" && activity != lastActivity {
			lastActivity = activity
			seq++
			_ = send(sse.Message{ID: seq, Data: SessionActivityEvent{Activity: activity}})
		}
	}

	// Stall detection: when the log hasn't grown for 5s, check the tmux
	// pane for a tool approval prompt. If found, emit a "pending" SSE event
	// so the UI can show the approval panel.
	var lastPendingID string
	onStall := func() {
		sp := s.state.SessionProvider()
		ip, ok := sp.(runtime.InteractionProvider)
		if !ok {
			return
		}
		pending, err := ip.Pending(info.SessionName)
		if err != nil || pending == nil {
			if lastPendingID != "" {
				// Approval cleared — emit activity update.
				lastPendingID = ""
				seq++
				_ = send(sse.Message{ID: seq, Data: SessionActivityEvent{Activity: "in-turn"}})
			}
			return
		}
		if pending.RequestID == lastPendingID {
			return // already emitted this approval
		}
		lastPendingID = pending.RequestID
		seq++
		_ = send(sse.Message{ID: seq, Data: *pending})
	}

	keepaliveTicker := time.NewTicker(sseKeepalive)
	defer keepaliveTicker.Stop()
	lw.Run(ctx, readAndEmit, func() {
		_ = send.Data(HeartbeatEvent{Timestamp: time.Now().UTC().Format(time.RFC3339)})
	}, RunOpts{
		OnStall:      onStall,
		StallTimeout: 5 * time.Second,
	})
}

func (s *Server) streamSessionTranscriptLog(ctx context.Context, send sse.Sender, info session.Info, logPath string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	send = cancelOnSendError(send, cancel)

	lw := newLogFileWatcher(logPath)
	defer lw.Close()

	var lastSize int64
	var lastSentUUID string
	var seq int
	var lastActivity string
	sentUUIDs := make(map[string]struct{})
	lw.onReset = func() { lastSize = 0; lastActivity = "" }

	readAndEmit := func() {
		stat, err := os.Stat(logPath)
		if err != nil {
			return
		}
		if stat.Size() == lastSize {
			return
		}

		sess, err := sessionlog.ReadProviderFile(info.Provider, logPath, 0)
		if err != nil {
			return
		}
		lastSize = stat.Size()

		// Compute activity early (used after turn emission).
		activity := sessionlog.InferActivityFromEntries(sess.Messages)

		turns := make([]outputTurn, 0, len(sess.Messages))
		uuids := make([]string, 0, len(sess.Messages))
		for _, entry := range sess.Messages {
			turn := entryToTurn(entry)
			if turn.Text == "" {
				continue
			}
			turns = append(turns, turn)
			uuids = append(uuids, entry.UUID)
		}

		// Emit turns if there are new ones.
		if len(turns) > 0 {
			var toSend []outputTurn

			if lastSentUUID == "" {
				// First emission: send everything.
				toSend = turns
			} else {
				found := false
				for i, uuid := range uuids {
					if uuid == lastSentUUID {
						toSend = turns[i+1:]
						found = true
						break
					}
				}
				if !found {
					// Cursor lost (DAG rewrite, compaction). Instead of
					// re-syncing from the beginning (which causes duplicate/
					// out-of-order messages on the client), emit only turns
					// we haven't previously sent.
					log.Printf("session stream: cursor %s lost, emitting only new turns", lastSentUUID)
					for i, uuid := range uuids {
						if _, seen := sentUUIDs[uuid]; !seen {
							toSend = append(toSend, turns[i])
						}
					}
				}
			}

			if len(toSend) > 0 {
				seq++
				_ = send(sse.Message{ID: seq, Data: SessionStreamMessageEvent{
					ID:       info.ID,
					Template: info.Template,
					Provider: info.Provider,
					Format:   "conversation",
					Turns:    toSend,
				}})
			}

			// Track all current UUIDs so cursor-lost can filter correctly.
			lastSentUUID = uuids[len(uuids)-1]
			for _, uuid := range uuids {
				sentUUIDs[uuid] = struct{}{}
			}
		}

		// Emit activity after content so clients receive data before state change.
		if activity != "" && activity != lastActivity {
			lastActivity = activity
			seq++
			_ = send(sse.Message{ID: seq, Data: SessionActivityEvent{Activity: activity}})
		}
	}

	lw.Run(ctx, readAndEmit, func() {
		_ = send.Data(HeartbeatEvent{Timestamp: time.Now().UTC().Format(time.RFC3339)})
	})
}

// streamSessionPeekRaw polls tmux pane content and wraps it as format=raw
// messages so MC's JSONL rendering pipeline can display terminal output
// (e.g. OAuth prompts, startup screens) when no transcript log exists yet.
func (s *Server) streamSessionPeekRaw(ctx context.Context, send sse.Sender, info session.Info) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	send = cancelOnSendError(send, cancel)

	sp := s.state.SessionProvider()
	poll := time.NewTicker(outputStreamPollInterval)
	defer poll.Stop()
	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()

	var lastOutput string
	var seq int

	var lastPeekPendingID string

	emitPeek := func() {
		if !sp.IsRunning(info.SessionName) {
			return
		}
		output, err := sp.Peek(info.SessionName, 100)
		if err != nil || output == lastOutput {
			return
		}
		lastOutput = output
		seq++

		if output == "" {
			return
		}

		// Wrap as a fake assistant message in raw JSONL format so MC's
		// translate_transcript_response handles it like a normal transcript.
		fakeMsg := map[string]any{
			"role": "assistant",
			"content": []map[string]string{
				{"type": "text", "text": output},
			},
		}
		_ = send(sse.Message{ID: seq, Data: SessionStreamRawMessageEvent{
			ID:       info.ID,
			Template: info.Template,
			Provider: info.Provider,
			Format:   "raw",
			Messages: wrapRawFrames([]any{fakeMsg}),
		}})

		// Check for approval prompts in the pane output we already have.
		if ip, ok := sp.(runtime.InteractionProvider); ok {
			pending, pErr := ip.Pending(info.SessionName)
			if pErr == nil && pending != nil && pending.RequestID != lastPeekPendingID {
				lastPeekPendingID = pending.RequestID
				seq++
				_ = send(sse.Message{ID: seq, Data: *pending})
			} else if pending == nil && lastPeekPendingID != "" {
				lastPeekPendingID = ""
			}
		}
	}

	emitPeek()

	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			emitPeek()
		case <-keepalive.C:
			_ = send.Data(HeartbeatEvent{Timestamp: time.Now().UTC().Format(time.RFC3339)})
		}
	}
}

func (s *Server) streamSessionPeek(ctx context.Context, send sse.Sender, info session.Info) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	send = cancelOnSendError(send, cancel)

	sp := s.state.SessionProvider()
	poll := time.NewTicker(outputStreamPollInterval)
	defer poll.Stop()
	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()

	var lastOutput string
	var seq int

	emitPeek := func() {
		if !sp.IsRunning(info.SessionName) {
			return
		}
		output, err := sp.Peek(info.SessionName, 100)
		if err != nil || output == lastOutput {
			return
		}
		lastOutput = output
		seq++

		turns := []outputTurn{}
		if output != "" {
			turns = append(turns, outputTurn{Role: "output", Text: output})
		}
		_ = send(sse.Message{ID: seq, Data: SessionStreamMessageEvent{
			ID:       info.ID,
			Template: info.Template,
			Provider: info.Provider,
			Format:   "text",
			Turns:    turns,
		}})
	}

	emitPeek()

	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			emitPeek()
		case <-keepalive.C:
			_ = send.Data(HeartbeatEvent{Timestamp: time.Now().UTC().Format(time.RFC3339)})
		}
	}
}
