// Package hooks installs the Claude city-level settings file that gc passes
// via --settings on session start. All other provider hook files ship from
// the core bootstrap pack's overlay/per-provider/<provider>/ tree and flow
// through the normal overlay copy+merge pipeline.
package hooks

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/overlay"
)

//go:embed config/claude.json
var configFS embed.FS

// supported lists provider names that Install recognizes. Only Claude has a
// city-level file; every other provider's hooks arrive via overlay copy.
var supported = []string{"claude"}

// overlayManaged lists provider names whose hooks ship via the core pack
// overlay instead of this package. Included in Validate's accept set so
// existing install_agent_hooks entries stay valid without extra config churn.
var overlayManaged = []string{"codex", "gemini", "opencode", "copilot", "cursor", "pi", "omp"}

// unsupported lists provider names that have no hook mechanism.
var unsupported = []string{"amp", "auggie"}

// SupportedProviders returns the list of provider names with hook support —
// including the overlay-managed ones so callers can surface them in docs.
func SupportedProviders() []string {
	out := make([]string, 0, len(supported)+len(overlayManaged))
	out = append(out, supported...)
	out = append(out, overlayManaged...)
	return out
}

// Validate checks that all provider names are supported for hook installation.
// Returns an error listing any unsupported names.
func Validate(providers []string) error {
	accept := make(map[string]bool, len(supported)+len(overlayManaged))
	for _, s := range supported {
		accept[s] = true
	}
	for _, s := range overlayManaged {
		accept[s] = true
	}
	noHook := make(map[string]bool, len(unsupported))
	for _, u := range unsupported {
		noHook[u] = true
	}
	var bad []string
	for _, p := range providers {
		if !accept[p] {
			if noHook[p] {
				bad = append(bad, fmt.Sprintf("%s (no hook mechanism)", p))
			} else {
				bad = append(bad, fmt.Sprintf("%s (unknown)", p))
			}
		}
	}
	if len(bad) > 0 {
		all := append(append([]string{}, supported...), overlayManaged...)
		return fmt.Errorf("unsupported install_agent_hooks: %s; supported: %s",
			strings.Join(bad, ", "), strings.Join(all, ", "))
	}
	return nil
}

// Install writes hook files that require Go-side wiring. Currently that is
// only Claude's city-level settings file — other providers flow through the
// core pack's overlay/per-provider/<provider>/ tree at session start.
// Entries for overlay-managed providers are accepted and silently no-op.
func Install(fs fsys.FS, cityDir, workDir string, providers []string) error {
	_ = workDir // reserved for future per-workdir installs
	for _, p := range providers {
		switch p {
		case "claude":
			if err := installClaude(fs, cityDir); err != nil {
				return fmt.Errorf("installing %s hooks: %w", p, err)
			}
		case "codex", "gemini", "opencode", "copilot", "cursor", "pi", "omp":
			// Shipped via core pack overlay — no Go-side work needed.
		default:
			return fmt.Errorf("unsupported hook provider %q", p)
		}
	}
	return nil
}

// installClaude writes both the legacy hook file (hooks/claude.json) and the
// runtime settings file (.gc/settings.json) in the city directory.
//
// Source precedence for user-authored Claude settings:
//  1. <city>/.claude/settings.json
//  2. <city>/hooks/claude.json
//  3. <city>/.gc/settings.json
//
// The selected source is merged onto the embedded default Claude settings, and
// the merged result is written to the managed outputs that Gas City points
// Claude's --settings flag at.
func installClaude(fs fsys.FS, cityDir string) error {
	hookDst := filepath.Join(cityDir, citylayout.ClaudeHookFile)
	runtimeDst := filepath.Join(cityDir, ".gc", "settings.json")
	data, sourceKind, err := desiredClaudeSettings(fs, cityDir)
	if err != nil {
		return err
	}

	if sourceKind != claudeSettingsSourceLegacyHook {
		if err := writeManagedFile(fs, hookDst, data); err != nil {
			return err
		}
	}
	return writeManagedFile(fs, runtimeDst, data)
}

func readEmbedded(embedPath string) ([]byte, error) {
	data, err := configFS.ReadFile(embedPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", embedPath, err)
	}
	return data, nil
}

type claudeSettingsSourceKind int

const (
	claudeSettingsSourceNone claudeSettingsSourceKind = iota
	claudeSettingsSourceCityDotClaude
	claudeSettingsSourceLegacyHook
	claudeSettingsSourceLegacyRuntime
)

func desiredClaudeSettings(fs fsys.FS, cityDir string) ([]byte, claudeSettingsSourceKind, error) {
	base, err := readEmbedded("config/claude.json")
	if err != nil {
		return nil, claudeSettingsSourceNone, err
	}

	overridePath, overrideData, sourceKind, err := readClaudeSettingsOverride(fs, cityDir, base)
	if err != nil {
		return nil, claudeSettingsSourceNone, err
	}
	if len(overrideData) == 0 {
		return base, claudeSettingsSourceNone, nil
	}
	if sourceKind != claudeSettingsSourceCityDotClaude {
		return overrideData, sourceKind, nil
	}

	merged, err := overlay.MergeSettingsJSON(base, overrideData)
	if err != nil {
		return nil, claudeSettingsSourceNone, fmt.Errorf("merging Claude settings from %s: %w", overridePath, err)
	}
	return merged, sourceKind, nil
}

func readClaudeSettingsOverride(fs fsys.FS, cityDir string, base []byte) (string, []byte, claudeSettingsSourceKind, error) {
	if path, data, ok, err := readClaudeSettingsCandidate(fs, citylayout.ClaudeSettingsPath(cityDir)); err != nil {
		return "", nil, claudeSettingsSourceNone, err
	} else if ok {
		return path, data, claudeSettingsSourceCityDotClaude, nil
	}

	hookPath := citylayout.ClaudeHookFilePath(cityDir)
	runtimePath := filepath.Join(cityDir, ".gc", "settings.json")
	_, hookData, hookExists, err := readClaudeSettingsCandidate(fs, hookPath)
	if err != nil {
		return "", nil, claudeSettingsSourceNone, err
	}
	_, runtimeData, runtimeExists, err := readClaudeSettingsCandidate(fs, runtimePath)
	if err != nil {
		return "", nil, claudeSettingsSourceNone, err
	}

	if hookExists &&
		!bytes.Equal(hookData, base) &&
		(!runtimeExists || !bytes.Equal(hookData, runtimeData)) &&
		!claudeFileNeedsUpgrade(hookData) {
		return hookPath, hookData, claudeSettingsSourceLegacyHook, nil
	}
	if runtimeExists &&
		!bytes.Equal(runtimeData, base) &&
		!claudeFileNeedsUpgrade(runtimeData) {
		return runtimePath, runtimeData, claudeSettingsSourceLegacyRuntime, nil
	}
	return "", nil, claudeSettingsSourceNone, nil
}

func readClaudeSettingsCandidate(fs fsys.FS, path string) (string, []byte, bool, error) {
	data, err := fs.ReadFile(path)
	if err == nil {
		return path, data, true, nil
	}
	if _, statErr := fs.Stat(path); statErr == nil {
		return "", nil, false, fmt.Errorf("reading %s: %w", path, err)
	}
	return "", nil, false, nil
}

func writeManagedFile(fs fsys.FS, dst string, data []byte) error {
	if existing, err := fs.ReadFile(dst); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
	} else if _, statErr := fs.Stat(dst); statErr == nil {
		// File exists but isn't readable. Preserve it rather than clobbering it.
		return nil
	}

	dir := filepath.Dir(dst)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	if err := fs.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}

func claudeFileNeedsUpgrade(existing []byte) bool {
	current, err := readEmbedded("config/claude.json")
	if err != nil {
		return false
	}
	stale := strings.Replace(string(current), `gc handoff "context cycle"`, `gc prime --hook`, 1)
	return string(existing) == stale
}
