package worker

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// ResolvedRuntime captures worker-owned launch inputs after a caller has
// resolved provider-specific runtime configuration.
type ResolvedRuntime struct {
	Command    string
	WorkDir    string
	Provider   string
	SessionEnv map[string]string
	Resume     sessionpkg.ProviderResume
	Hints      runtime.Config
}

// ResolvedSessionConfig describes a new session-backed worker handle whose
// runtime inputs have already been resolved by the caller.
type ResolvedSessionConfig struct {
	Alias        string
	ExplicitName string
	Template     string
	Title        string
	Transport    string
	Metadata     map[string]string
	Runtime      ResolvedRuntime
}

// SessionSpecForResolvedRuntime translates resolved runtime inputs into the
// canonical worker session spec used by session-backed handles.
func SessionSpecForResolvedRuntime(cfg ResolvedSessionConfig) (SessionSpec, error) {
	command := strings.TrimSpace(cfg.Runtime.Command)
	if command == "" {
		return SessionSpec{}, fmt.Errorf("%w: command is required", ErrHandleConfig)
	}

	provider := strings.TrimSpace(cfg.Runtime.Provider)
	if provider == "" {
		provider = command
		if idx := strings.IndexAny(provider, " \t"); idx >= 0 {
			provider = provider[:idx]
		}
	}
	if provider == "" {
		return SessionSpec{}, fmt.Errorf("%w: provider is required", ErrHandleConfig)
	}

	workDir := strings.TrimSpace(cfg.Runtime.WorkDir)
	hints := cloneRuntimeConfig(cfg.Runtime.Hints)
	if workDir == "" {
		workDir = strings.TrimSpace(hints.WorkDir)
	}
	if strings.TrimSpace(hints.WorkDir) == "" {
		hints.WorkDir = workDir
	}

	return SessionSpec{
		Alias:        cfg.Alias,
		ExplicitName: cfg.ExplicitName,
		Template:     cfg.Template,
		Title:        cfg.Title,
		Command:      command,
		WorkDir:      workDir,
		Provider:     provider,
		Transport:    strings.TrimSpace(cfg.Transport),
		Env:          cloneStringMap(cfg.Runtime.SessionEnv),
		Resume:       cfg.Runtime.Resume,
		Hints:        hints,
		Metadata:     cloneStringMap(cfg.Metadata),
	}, nil
}

// SessionForResolvedRuntime constructs a session-backed handle from caller-
// resolved runtime inputs without forcing the caller to rebuild SessionSpec.
func (f *Factory) SessionForResolvedRuntime(cfg ResolvedSessionConfig) (Handle, error) {
	spec, err := SessionSpecForResolvedRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return f.Session(spec)
}
