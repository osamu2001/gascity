package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/shellquote"
	"github.com/gastownhall/gascity/internal/worker"
)

var errSessionTemplateNotFound = errors.New("session template not found")

type sessionCreateRequest struct {
	// Kind discriminates the session target: "agent" or "provider".
	Kind              string            `json:"kind,omitempty"`
	Name              string            `json:"name,omitempty"`
	Alias             string            `json:"alias,omitempty"`
	LegacySessionName *string           `json:"session_name,omitempty"`
	Message           string            `json:"message,omitempty"`
	Async             bool              `json:"async,omitempty"`
	Options           map[string]string `json:"options,omitempty"`
	// ProjectID is an opaque identifier for the MC project context.
	// Stored in bead metadata for session-to-project association.
	ProjectID string `json:"project_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

type sessionMessageRequest struct {
	Message string `json:"message"`
}

type sessionPendingResponse struct {
	Supported bool                        `json:"supported"`
	Pending   *runtime.PendingInteraction `json:"pending,omitempty"`
}

type sessionRespondRequest struct {
	RequestID string            `json:"request_id,omitempty"`
	Action    string            `json:"action"`
	Text      string            `json:"text,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type sessionTranscriptResponse struct {
	ID         string                       `json:"id"`
	Template   string                       `json:"template"`
	Format     string                       `json:"format"`
	Turns      []outputTurn                 `json:"turns"`
	Pagination *worker.TranscriptPagination `json:"pagination,omitempty"`
}

type sessionRawTranscriptResponse struct {
	ID         string                       `json:"id"`
	Template   string                       `json:"template"`
	Format     string                       `json:"format"`
	Messages   []json.RawMessage            `json:"messages"`
	Pagination *worker.TranscriptPagination `json:"pagination,omitempty"`
}

func writeSessionManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrInvalidSessionName):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, session.ErrSessionNameExists):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, session.ErrInvalidSessionAlias):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, session.ErrSessionAliasExists):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, session.ErrInteractionUnsupported):
		writeError(w, http.StatusNotImplemented, "unsupported", err.Error())
	case errors.Is(err, session.ErrPendingInteraction):
		writeError(w, http.StatusConflict, "pending_interaction", err.Error())
	case errors.Is(err, session.ErrNoPendingInteraction):
		writeError(w, http.StatusConflict, "no_pending", err.Error())
	case errors.Is(err, session.ErrInteractionMismatch):
		writeError(w, http.StatusConflict, "invalid_interaction", err.Error())
	case errors.Is(err, session.ErrSessionClosed), errors.Is(err, session.ErrResumeRequired):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, session.ErrNotSession):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	default:
		writeStoreError(w, err)
	}
}

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	store := s.state.CityBeadStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no bead store configured")
		return
	}

	var body sessionCreateRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if body.LegacySessionName != nil {
		writeError(w, http.StatusBadRequest, "invalid", "session_name is no longer accepted; use alias")
		return
	}

	kind := body.Kind
	name := body.Name
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "name is required")
		return
	}
	if kind != "agent" && kind != "provider" {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be 'agent' or 'provider'")
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

	var resolved *config.ResolvedProvider
	var workDir, transport, template string
	var optMeta map[string]string

	switch kind {
	case "agent":
		var err error
		resolved, workDir, transport, template, err = s.resolveSessionTemplate(name)
		if err != nil {
			if errors.Is(err, errSessionTemplateNotFound) {
				s.idem.unreserve(idemKey)
				writeError(w, http.StatusNotFound, "agent_not_found", "agent '"+name+"' not found")
				return
			}
			s.idem.unreserve(idemKey)
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		// Agent track: command comes from the agent config as-is.
		// Do NOT inject OptionsSchema defaults — agents encode their own CLI flags.
		// Options are stored as template_overrides and applied at start time
		// by the session lifecycle via ResolveExplicitOptions.
		if len(body.Options) > 0 {
			if len(resolved.OptionsSchema) == 0 {
				s.idem.unreserve(idemKey)
				writeError(w, http.StatusBadRequest, "unknown_option", "agent '"+name+"' does not accept options")
				return
			}
			// Validate options against the schema without applying defaults.
			if _, err := config.ResolveExplicitOptions(resolved.OptionsSchema, body.Options); err != nil {
				s.idem.unreserve(idemKey)
				if errors.Is(err, config.ErrUnknownOption) {
					writeError(w, http.StatusBadRequest, "unknown_option", err.Error())
					return
				}
				writeError(w, http.StatusBadRequest, "invalid_option_value", err.Error())
				return
			}
		}

	case "provider":
		s.createProviderSession(w, r, store, body, name, idemKey, bodyHash)
		return
	}

	title := body.Title
	if title == "" {
		title = template
	}

	resume := session.ProviderResume{
		ResumeFlag:    resolved.ResumeFlag,
		ResumeStyle:   resolved.ResumeStyle,
		ResumeCommand: resolved.ResumeCommand,
		SessionIDFlag: resolved.SessionIDFlag,
	}
	alias, err := session.ValidateAlias(body.Alias)
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	// Merge explicit options with provider effective defaults.
	// User-specified options override defaults; unspecified options get the
	// provider's EffectiveDefaults (e.g., permission_mode=unrestricted for claude).
	command := resolved.CommandString()
	if len(resolved.OptionsSchema) > 0 {
		mergedOptions := make(map[string]string)
		for k, v := range resolved.EffectiveDefaults {
			mergedOptions[k] = v
		}
		for k, v := range body.Options {
			mergedOptions[k] = v
		}
		if mergedArgs, err := config.ResolveExplicitOptions(resolved.OptionsSchema, mergedOptions); err == nil && len(mergedArgs) > 0 {
			command = config.ReplaceSchemaFlags(command, resolved.OptionsSchema, mergedArgs)
		}
	}

	// Build template_overrides metadata. Includes schema overrides AND
	// the initial message (as "initial_message" key). The reconciler
	// handles both: schema overrides map to CLI flags, initial_message
	// is appended to the prompt on first start only.
	allOverrides := make(map[string]string)
	for k, v := range body.Options {
		allOverrides[k] = v
	}
	if msg := strings.TrimSpace(body.Message); msg != "" {
		allOverrides["initial_message"] = msg
	}
	var extraMeta map[string]string
	if len(allOverrides) > 0 {
		if overridesJSON, jsonErr := json.Marshal(allOverrides); jsonErr == nil {
			extraMeta = map[string]string{"template_overrides": string(overridesJSON)}
		}
	}
	if extraMeta == nil {
		extraMeta = make(map[string]string)
	}
	extraMeta["session_origin"] = "ephemeral"

	// Agent sessions always use async (bead-only) creation. The reconciler
	// starts the agent process on the next tick. This avoids blocking the
	// HTTP response for 10-30s while the agent boots in tmux, and lets MC
	// show the session in the sidebar immediately via optimistic UI.
	handle, err := s.newWorkerSessionHandle(store, worker.SessionSpec{
		Alias:     alias,
		Template:  template,
		Title:     title,
		Command:   command,
		WorkDir:   workDir,
		Provider:  resolved.Name,
		Transport: transport,
		Env:       resolved.Env,
		Resume:    resume,
		Hints:     sessionCreateHints(resolved),
		Metadata:  extraMeta,
	})
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}
	var info session.Info
	err = session.WithCitySessionAliasLock(s.state.CityPath(), alias, func() error {
		if err := session.EnsureAliasAvailableWithConfig(store, s.state.Config(), alias, ""); err != nil {
			return err
		}
		var createErr error
		info, createErr = handle.Create(r.Context(), worker.CreateModeDeferred)
		return createErr
	})
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	// Persist kind, option metadata, and project_id on the bead.
	// NOTE: template_overrides (options + initial_message) is already set via
	// extraMeta in CreateAliasedBeadOnlyNamedWithMetadata above. Do NOT
	// overwrite it here — the old code clobbered initial_message by writing
	// only the options portion.
	s.persistSessionMeta(store, info.ID, "agent", body.ProjectID, optMeta)
	s.state.Poke() // wake reconciler to start the agent

	// Auto-generate a title from the user's message if no explicit title was provided.
	titleProvider := s.resolveTitleProvider()
	MaybeGenerateTitleAsync(store, info.ID, body.Title, body.Message, titleProvider, info.WorkDir, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "session %s: "+format+"\n", append([]any{info.ID}, args...)...)
	})

	resp := sessionToResponse(info, s.state.Config())
	resp.Kind = "agent"
	if catalog, catErr := s.workerSessionCatalog(store); catErr == nil {
		if caps, capErr := catalog.SubmissionCapabilities(info.ID); capErr == nil {
			resp.SubmissionCapabilities = caps
		}
	}
	if handle, handleErr := s.workerHandleForSession(store, info.ID); handleErr == nil {
		s.enrichSessionResponse(&resp, info, s.state.Config(), handle, false)
	}
	statusCode := http.StatusAccepted // always async for agent sessions
	s.idem.storeResponse(idemKey, bodyHash, statusCode, resp)
	writeJSON(w, statusCode, resp)
}

// createProviderSession handles the "provider" kind session creation.
// Resolves a bare provider (not an agent template) and creates a session.
func (s *Server) createProviderSession(w http.ResponseWriter, r *http.Request, store beads.Store, body sessionCreateRequest, providerName, idemKey, bodyHash string) {
	cfg := s.state.Config()
	if cfg == nil {
		s.idem.unreserve(idemKey)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "city config not loaded yet")
		return
	}
	resolved, err := config.ResolveProvider(
		&config.Agent{Provider: providerName},
		&cfg.Workspace,
		cfg.Providers,
		exec.LookPath,
	)
	if err != nil {
		s.idem.unreserve(idemKey)
		if errors.Is(err, config.ErrProviderNotInPATH) {
			writeError(w, http.StatusServiceUnavailable, "provider_unavailable", err.Error())
			return
		}
		if errors.Is(err, config.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "provider_not_found", "provider '"+providerName+"' not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Resolve options against the provider's schema.
	var extraArgs []string
	var optMeta map[string]string
	if len(body.Options) > 0 && len(resolved.OptionsSchema) == 0 {
		s.idem.unreserve(idemKey)
		writeError(w, http.StatusBadRequest, "unknown_option", "provider '"+providerName+"' does not accept options")
		return
	}
	if len(resolved.OptionsSchema) > 0 {
		var optErr error
		extraArgs, optMeta, optErr = config.ResolveOptions(resolved.OptionsSchema, body.Options, resolved.EffectiveDefaults)
		if optErr != nil {
			s.idem.unreserve(idemKey)
			if errors.Is(optErr, config.ErrUnknownOption) {
				writeError(w, http.StatusBadRequest, "unknown_option", optErr.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_option_value", optErr.Error())
			return
		}
	}

	template := providerName
	title := body.Title
	if title == "" {
		title = resolved.Name
	}
	if body.Async && strings.TrimSpace(body.Message) != "" {
		s.idem.unreserve(idemKey)
		writeError(w, http.StatusBadRequest, "invalid", "message is not supported with async session creation; create the session, then POST /v0/session/{id}/messages")
		return
	}
	if body.Async {
		s.idem.unreserve(idemKey)
		writeError(w, http.StatusBadRequest, "invalid", "async session creation is only supported for configured agent templates")
		return
	}

	workDir := s.state.CityPath()

	resume := session.ProviderResume{
		ResumeFlag:    resolved.ResumeFlag,
		ResumeStyle:   resolved.ResumeStyle,
		ResumeCommand: resolved.ResumeCommand,
		SessionIDFlag: resolved.SessionIDFlag,
	}
	alias, err := session.ValidateAlias(body.Alias)
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	command := resolved.CommandString()
	if len(extraArgs) > 0 {
		command = command + " " + shellquote.Join(extraArgs)
	}

	hints := sessionCreateHints(resolved)
	handle, err := s.newWorkerSessionHandle(store, worker.SessionSpec{
		Alias:     alias,
		Template:  template,
		Title:     title,
		Command:   command,
		WorkDir:   workDir,
		Provider:  resolved.Name,
		Transport: "",
		Env:       resolved.Env,
		Resume:    resume,
		Hints:     hints,
		Metadata: map[string]string{
			"session_origin": "manual",
		},
	})
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}
	var info session.Info
	err = session.WithCitySessionAliasLock(s.state.CityPath(), alias, func() error {
		if err := session.EnsureAliasAvailableWithConfig(store, s.state.Config(), alias, ""); err != nil {
			return err
		}
		var createErr error
		info, createErr = handle.Create(r.Context(), worker.CreateModeStarted)
		return createErr
	})
	if err != nil {
		s.idem.unreserve(idemKey)
		writeSessionManagerError(w, err)
		return
	}

	// Persist kind, option metadata, and project_id on the bead.
	s.persistSessionMeta(store, info.ID, "provider", body.ProjectID, optMeta)
	if body.Async {
		s.state.Poke()
	}

	// Auto-generate a title from the user's message if no explicit title was provided.
	titleProvider := s.resolveTitleProvider()
	MaybeGenerateTitleAsync(store, info.ID, body.Title, body.Message, titleProvider, info.WorkDir, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "session %s: "+format+"\n", append([]any{info.ID}, args...)...)
	})

	// Deliver initial message if provided.
	if msg := strings.TrimSpace(body.Message); msg != "" {
		if _, sendErr := s.submitMessageToSession(r.Context(), store, info.ID, msg, session.SubmitIntentDefault); sendErr != nil {
			log.Printf("session %s: initial message delivery failed: %v", info.ID, sendErr)
			s.idem.unreserve(idemKey)
			writeError(w, http.StatusInternalServerError, "message_delivery_failed",
				fmt.Sprintf("session created but initial message failed: %v", sendErr))
			return
		}
	}

	resp := sessionToResponse(info, s.state.Config())
	resp.Kind = "provider"
	if catalog, catErr := s.workerSessionCatalog(store); catErr == nil {
		if caps, capErr := catalog.SubmissionCapabilities(info.ID); capErr == nil {
			resp.SubmissionCapabilities = caps
		}
	}
	if handle, handleErr := s.workerHandleForSession(store, info.ID); handleErr == nil {
		s.enrichSessionResponse(&resp, info, s.state.Config(), handle, false)
	}
	statusCode := http.StatusCreated
	s.idem.storeResponse(idemKey, bodyHash, statusCode, resp)
	writeJSON(w, statusCode, resp)
}

// persistSessionMeta writes option metadata and project_id to the session bead.
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

	catalog, err := s.workerSessionCatalog(store)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	info, err := catalog.Get(id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	handle, err := s.workerHandleForSession(store, id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	path, err := handle.TranscriptPath(r.Context())
	if err != nil && !errors.Is(err, worker.ErrHistoryUnavailable) {
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
			transcript, err := handle.Transcript(r.Context(), worker.TranscriptRequest{
				TailCompactions: tail,
				BeforeEntryID:   before,
				Raw:             true,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "reading session log: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, sessionRawTranscriptResponse{
				ID:         info.ID,
				Template:   info.Template,
				Format:     "raw",
				Messages:   transcript.RawMessages,
				Pagination: transcript.Session.Pagination,
			})
			return
		}

		transcript, err := handle.Transcript(r.Context(), worker.TranscriptRequest{
			TailCompactions: tail,
			BeforeEntryID:   before,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "reading session log: "+err.Error())
			return
		}
		sess := transcript.Session

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

	output, peekErr := handle.Peek(r.Context(), 100)
	if peekErr != nil && !errors.Is(peekErr, session.ErrSessionInactive) {
		writeError(w, http.StatusInternalServerError, "internal", peekErr.Error())
		return
	}
	if peekErr == nil {
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

	handle, err := s.workerHandleForSession(store, id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	if err := handle.Kill(r.Context()); err != nil {
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

	handle, err := s.workerHandleForSession(store, id)
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	pending, supported, err := handle.PendingStatus(r.Context())
	if err != nil {
		writeSessionManagerError(w, err)
		return
	}
	var pendingResp *runtime.PendingInteraction
	if pending != nil {
		pendingResp = &runtime.PendingInteraction{
			RequestID: pending.RequestID,
			Kind:      pending.Kind,
			Prompt:    pending.Prompt,
			Options:   append([]string(nil), pending.Options...),
			Metadata:  cloneStringMap(pending.Metadata),
		}
	}
	writeJSON(w, http.StatusOK, sessionPendingResponse{
		Supported: supported,
		Pending:   pendingResp,
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
