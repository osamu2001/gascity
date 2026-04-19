package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Start ensures the worker exists and its runtime is live.
func (h *SessionHandle) Start(ctx context.Context) error {
	id, err := h.ensureSessionID()
	if err != nil {
		return err
	}
	startCommand, err := h.startCommand(id)
	if err != nil {
		return err
	}
	return h.manager.Start(ctx, id, startCommand, h.runtimeHints())
}

// StartResolved starts or resumes the worker using a caller-supplied runtime
// command and hints. This is a migration bridge for higher layers that already
// materialize provider-specific runtime config but should still delegate the
// provider-specific runtime bring-up through the worker boundary.
func (h *SessionHandle) StartResolved(ctx context.Context, startCommand string, hints runtime.Config) error {
	id, err := h.ensureSessionID()
	if err != nil {
		return err
	}
	command := strings.TrimSpace(startCommand)
	if command == "" {
		command, err = h.startCommand(id)
		if err != nil {
			return err
		}
	}
	startHints := hints
	if strings.TrimSpace(startHints.Command) == "" {
		startHints = h.runtimeHints()
	}
	return h.manager.StartRuntimeOnly(ctx, id, command, startHints)
}

// Attach ensures the worker runtime is live and then attaches the caller's
// terminal using the underlying session transport.
func (h *SessionHandle) Attach(ctx context.Context) error {
	id, err := h.ensureSessionID()
	if err != nil {
		return err
	}
	resumeCommand, err := h.startCommand(id)
	if err != nil {
		return err
	}
	return h.manager.Attach(ctx, id, resumeCommand, h.runtimeHints())
}

// Create materializes the worker session without requiring API callers to
// invoke session.Manager lifecycle methods directly.
func (h *SessionHandle) Create(ctx context.Context, mode CreateMode) (sessionpkg.Info, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.sessionID != "" {
		return h.manager.Get(h.sessionID)
	}

	switch mode {
	case CreateModeDeferred:
		return h.createDeferredLocked()
	case CreateModeStarted:
		return h.createStartedLocked(ctx)
	default:
		return sessionpkg.Info{}, fmt.Errorf("%w: unknown create mode %q", ErrHandleConfig, mode)
	}
}

// Reset requests a fresh restart for the worker while preserving the bead.
func (h *SessionHandle) Reset(context.Context) error {
	id := h.currentSessionID()
	if id == "" {
		return fmt.Errorf("%w: reset requires an existing bead-backed session", ErrOperationUnsupported)
	}
	return h.manager.RequestFreshRestart(id)
}

// Stop suspends the worker runtime while preserving conversation state.
func (h *SessionHandle) Stop(context.Context) error {
	id := h.currentSessionID()
	if id == "" {
		return nil
	}
	return h.manager.Suspend(id)
}

// Kill terminates the live runtime without mutating the persisted lifecycle.
func (h *SessionHandle) Kill(context.Context) error {
	id := h.currentSessionID()
	if id == "" {
		return nil
	}
	return h.manager.Kill(id)
}

// Close permanently ends the worker session.
func (h *SessionHandle) Close(context.Context) error {
	id := h.currentSessionID()
	if id == "" {
		return nil
	}
	return h.manager.Close(id)
}

// Rename updates the user-facing session title.
func (h *SessionHandle) Rename(_ context.Context, title string) error {
	id := h.currentSessionID()
	if id == "" {
		return nil
	}
	return h.manager.Rename(id, strings.TrimSpace(title))
}

// Peek captures recent provider output without attaching.
func (h *SessionHandle) Peek(_ context.Context, lines int) (string, error) {
	id := h.currentSessionID()
	if id == "" {
		return "", sessionpkg.ErrSessionInactive
	}
	return h.manager.Peek(id, lines)
}

// State returns the worker-level lifecycle view.
func (h *SessionHandle) State(ctx context.Context) (State, error) {
	id := h.currentSessionID()
	if id == "" {
		return State{Phase: PhaseStopped, Provider: h.providerLabel()}, nil
	}

	info, err := h.manager.Get(id)
	if err != nil {
		return State{}, err
	}
	state := State{
		SessionID:   info.ID,
		SessionName: info.SessionName,
		Provider:    h.providerLabel(),
		Detail:      string(info.State),
	}

	switch info.State {
	case sessionpkg.StateCreating:
		state.Phase = PhaseStarting
		return state, nil
	case sessionpkg.StateDraining:
		state.Phase = PhaseStopping
		return state, nil
	case sessionpkg.StateAsleep, sessionpkg.StateSuspended, sessionpkg.StateDrained, sessionpkg.StateArchived:
		state.Phase = PhaseStopped
		return state, nil
	case sessionpkg.StateQuarantined:
		pending, err := h.Pending(ctx)
		if err != nil {
			return State{}, err
		}
		state.Phase = PhaseBlocked
		state.Pending = pending
		return state, nil
	case sessionpkg.StateActive, sessionpkg.StateAwake:
		pending, err := h.Pending(ctx)
		if err != nil {
			return State{}, err
		}
		if pending != nil {
			state.Phase = PhaseBlocked
			state.Pending = pending
			return state, nil
		}
		state.Phase = PhaseReady
		if history, histErr := h.History(ctx, HistoryRequest{}); histErr == nil && history != nil && history.TailState.Activity == TailActivityInTurn {
			state.Phase = PhaseBusy
		}
		return state, nil
	default:
		if info.Closed {
			state.Phase = PhaseStopped
			return state, nil
		}
		state.Phase = PhaseUnknown
	}

	return state, nil
}

// Message sends a user turn to the worker.
func (h *SessionHandle) Message(ctx context.Context, req MessageRequest) (MessageResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return MessageResult{}, fmt.Errorf("message text is required")
	}
	id, err := h.ensureSessionID()
	if err != nil {
		return MessageResult{}, err
	}
	resumeCommand, err := h.startCommand(id)
	if err != nil {
		return MessageResult{}, err
	}
	outcome, err := h.manager.Submit(ctx, id, req.Text, resumeCommand, h.runtimeHints(), submitIntent(req.Delivery))
	if err != nil {
		return MessageResult{}, err
	}
	return MessageResult{Queued: outcome.Queued}, nil
}

// Interrupt soft-stops any in-flight worker turn.
func (h *SessionHandle) Interrupt(context.Context, InterruptRequest) error {
	id := h.currentSessionID()
	if id == "" {
		return nil
	}
	return h.manager.StopTurn(id)
}

// Nudge sends a best-effort redirect message to the worker.
func (h *SessionHandle) Nudge(ctx context.Context, req NudgeRequest) (NudgeResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return NudgeResult{}, fmt.Errorf("nudge text is required")
	}
	id, err := h.ensureSessionID()
	if err != nil {
		return NudgeResult{}, err
	}
	resumeCommand, err := h.startCommand(id)
	if err != nil {
		return NudgeResult{}, err
	}
	switch req.Delivery {
	case "", NudgeDeliveryDefault:
		if normalizeNudgeWakePolicy(req.Wake) == NudgeWakeLiveOnly {
			delivered, err := h.manager.SendLiveOnly(ctx, id, req.Text)
			if err != nil {
				return NudgeResult{}, err
			}
			return NudgeResult{Delivered: delivered}, nil
		}
		if err := h.manager.Send(ctx, id, req.Text, resumeCommand, h.runtimeHints()); err != nil {
			return NudgeResult{}, err
		}
		return NudgeResult{Delivered: true}, nil
	case NudgeDeliveryImmediate:
		if normalizeNudgeWakePolicy(req.Wake) == NudgeWakeLiveOnly {
			delivered, err := h.manager.SendImmediateLiveOnly(ctx, id, req.Text)
			if err != nil {
				return NudgeResult{}, err
			}
			return NudgeResult{Delivered: delivered}, nil
		}
		if err := h.manager.SendImmediate(ctx, id, req.Text, resumeCommand, h.runtimeHints()); err != nil {
			return NudgeResult{}, err
		}
		return NudgeResult{Delivered: true}, nil
	case NudgeDeliveryWaitIdle:
		if normalizeNudgeWakePolicy(req.Wake) == NudgeWakeLiveOnly {
			delivered, err := h.manager.TryWaitIdleNudgeLiveOnly(ctx, id, req.Source, req.Text)
			if err != nil {
				return NudgeResult{}, err
			}
			return NudgeResult{Delivered: delivered}, nil
		}
		delivered, err := h.manager.TryWaitIdleNudge(ctx, id, req.Source, req.Text, resumeCommand, h.runtimeHints())
		if err != nil {
			return NudgeResult{}, err
		}
		return NudgeResult{Delivered: delivered}, nil
	default:
		return NudgeResult{}, fmt.Errorf("unknown nudge delivery %q", req.Delivery)
	}
}

func (h *SessionHandle) ensureSessionID() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessionID != "" {
		return h.sessionID, nil
	}
	info, err := h.createDeferredLocked()
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

func (h *SessionHandle) createDeferredLocked() (sessionpkg.Info, error) {
	info, err := h.manager.CreateAliasedBeadOnlyNamedWithMetadata(
		h.session.Alias,
		h.session.ExplicitName,
		h.session.Template,
		h.session.Title,
		h.session.Command,
		h.session.WorkDir,
		h.session.Provider,
		h.session.Transport,
		h.session.Resume,
		cloneStringMap(h.session.Metadata),
	)
	if err != nil {
		return sessionpkg.Info{}, err
	}
	h.sessionID = info.ID
	return info, nil
}

func (h *SessionHandle) createStartedLocked(ctx context.Context) (sessionpkg.Info, error) {
	info, err := h.manager.CreateAliasedNamedWithTransportAndMetadata(
		ctx,
		h.session.Alias,
		h.session.ExplicitName,
		h.session.Template,
		h.session.Title,
		h.session.Command,
		h.session.WorkDir,
		h.session.Provider,
		h.session.Transport,
		cloneStringMap(h.session.Env),
		h.session.Resume,
		cloneRuntimeConfig(h.session.Hints),
		cloneStringMap(h.session.Metadata),
	)
	if err != nil {
		return sessionpkg.Info{}, err
	}
	h.sessionID = info.ID
	return info, nil
}

func (h *SessionHandle) currentSessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionID
}

func (h *SessionHandle) startCommand(id string) (string, error) {
	info, err := h.manager.Get(id)
	if err != nil {
		return "", err
	}
	if info.State == sessionpkg.StateCreating && h.session.Resume.SessionIDFlag != "" && strings.TrimSpace(info.SessionKey) != "" {
		command := strings.TrimSpace(info.Command)
		if command == "" {
			command = strings.TrimSpace(h.session.Command)
		}
		if command == "" {
			command = strings.TrimSpace(info.Provider)
		}
		if command == "" {
			command = strings.TrimSpace(h.session.Provider)
		}
		if command == "" {
			return "", fmt.Errorf("%w: command is required for first start", ErrHandleConfig)
		}
		return command + " " + h.session.Resume.SessionIDFlag + " " + info.SessionKey, nil
	}
	resumeInfo := info
	if command := strings.TrimSpace(h.session.Command); command != "" {
		resumeInfo.Command = command
	}
	if provider := strings.TrimSpace(h.session.Provider); provider != "" {
		resumeInfo.Provider = provider
	}
	if resumeFlag := strings.TrimSpace(h.session.Resume.ResumeFlag); resumeFlag != "" {
		resumeInfo.ResumeFlag = resumeFlag
	}
	if resumeStyle := strings.TrimSpace(h.session.Resume.ResumeStyle); resumeStyle != "" {
		resumeInfo.ResumeStyle = resumeStyle
	}
	if resumeCommand := strings.TrimSpace(h.session.Resume.ResumeCommand); resumeCommand != "" {
		resumeInfo.ResumeCommand = resumeCommand
	}
	return sessionpkg.BuildResumeCommand(resumeInfo), nil
}

func (h *SessionHandle) providerLabel() string {
	if h.session.Profile != "" {
		return string(h.session.Profile)
	}
	return h.session.Provider
}

func (h *SessionHandle) historyProvider(info sessionpkg.Info) string {
	if h.session.Profile != "" {
		return string(h.session.Profile)
	}
	if strings.TrimSpace(info.Provider) != "" {
		return info.Provider
	}
	return h.session.Provider
}

func (h *SessionHandle) runtimeHints() runtime.Config {
	cfg := cloneRuntimeConfig(h.session.Hints)
	cfg.Env = mergeStringMaps(cfg.Env, h.session.Env)
	return cfg
}

func submitIntent(intent DeliveryIntent) sessionpkg.SubmitIntent {
	switch intent {
	case DeliveryIntentFollowUp:
		return sessionpkg.SubmitIntentFollowUp
	case DeliveryIntentInterruptNow:
		return sessionpkg.SubmitIntentInterruptNow
	default:
		return sessionpkg.SubmitIntentDefault
	}
}
