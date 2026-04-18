// Package hooks installs the Claude city-level settings file that gc passes
// via --settings on session start. All other provider hook files ship from
// the core bootstrap pack's overlay/per-provider/<provider>/ tree and flow
// through the normal overlay copy+merge pipeline.
package hooks

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
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

// installClaude writes the runtime settings file (.gc/settings.json) that gc
// passes to Claude via --settings. The legacy hooks/claude.json file is
// treated as user-owned whenever it contains content gc cannot recognize as
// its own: present and not matching a known stale auto-generated pattern.
// That file is rewritten only when it IS the selected source (legacy-hook
// migration), when it doesn't exist (fresh install seed), or when it matches
// a known stale pattern (safe auto-upgrade).
//
// Source precedence for user-authored Claude settings:
//  1. <city>/.claude/settings.json
//  2. <city>/hooks/claude.json
//  3. <city>/.gc/settings.json
//
// The selected source (or embedded defaults, if no override exists) is merged
// onto the embedded default Claude settings so new default hooks added in
// future releases land for users on every source, not just .claude/settings.json.
func installClaude(fs fsys.FS, cityDir string) error {
	hookDst := filepath.Join(cityDir, citylayout.ClaudeHookFile)
	runtimeDst := filepath.Join(cityDir, ".gc", "settings.json")
	data, sourceKind, err := desiredClaudeSettings(fs, cityDir)
	if err != nil {
		return err
	}

	if sourceKind == claudeSettingsSourceLegacyHook || hookFileSafeToRewrite(fs, hookDst) {
		if err := writeManagedFile(fs, hookDst, data); err != nil {
			return err
		}
	}
	return writeManagedFile(fs, runtimeDst, data)
}

// hookFileSafeToRewrite reports whether hooks/claude.json can be safely
// overwritten by installClaude without clobbering user-owned content. It is
// safe when the file does not exist (fresh install seed) or when its bytes
// match a known stale auto-generated pattern (proactive upgrade of leftover
// state). Any other content — including existing-but-unreadable files,
// content equal to the embedded base, or user-authored content — is
// preserved.
func hookFileSafeToRewrite(fs fsys.FS, hookDst string) bool {
	data, err := fs.ReadFile(hookDst)
	if err == nil {
		return claudeFileNeedsUpgrade(data)
	}
	// Only a genuine "not found" means we can safely seed. Any other read
	// error (permission, i/o) is an existing file in an unknown state —
	// preserve it rather than risk clobbering user content.
	return errors.Is(err, os.ErrNotExist)
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

// desiredClaudeSettings returns the bytes that should land in the managed
// runtime file (.gc/settings.json) and the source kind that was chosen.
//
// All override sources — including legacy ones — are merged against the
// embedded base. The hooks array in overlay.MergeSettingsJSON uses
// union-by-identity semantics (duplicate entries collapse), so merging is
// safe and gives legacy users the future-base-hook-additions path back: any
// new default hook added to config/claude.json in a future release lands
// for users whose source is hooks/claude.json or .gc/settings.json, not
// just users on .claude/settings.json.
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

	merged, err := overlay.MergeSettingsJSON(base, overrideData)
	if err != nil {
		return nil, claudeSettingsSourceNone, fmt.Errorf("merging Claude settings from %s: %w", overridePath, err)
	}
	return merged, sourceKind, nil
}

func readClaudeSettingsOverride(fs fsys.FS, cityDir string, base []byte) (string, []byte, claudeSettingsSourceKind, error) {
	// The preferred source (.claude/settings.json) is strict: if the user
	// placed the file and it can't be read, that's a hard error — we don't
	// want to silently fall back to a legacy source they didn't intend.
	if path, data, ok, err := readClaudeSettingsCandidate(fs, citylayout.ClaudeSettingsPath(cityDir), true); err != nil {
		return "", nil, claudeSettingsSourceNone, err
	} else if ok {
		return path, data, claudeSettingsSourceCityDotClaude, nil
	}

	// Legacy candidates are tolerant: an unreadable leftover file (bad perms,
	// partial write from a crashed tick) must not block source selection when
	// a readable higher-priority source or embedded defaults can be used.
	hookPath := citylayout.ClaudeHookFilePath(cityDir)
	runtimePath := filepath.Join(cityDir, ".gc", "settings.json")
	_, hookData, hookExists, _ := readClaudeSettingsCandidate(fs, hookPath, false)
	_, runtimeData, runtimeExists, _ := readClaudeSettingsCandidate(fs, runtimePath, false)

	// hooks/claude.json is authoritative when it exists, is not a known
	// stale auto-generated file, and differs from the managed runtime file
	// (the redundant-mirror case). We deliberately do NOT disqualify a
	// hook file whose bytes equal the embedded base: a user may pin
	// hooks/claude.json to exactly the embedded defaults as their
	// authoritative source and still expect it to outrank .gc/settings.json
	// per the documented precedence. Use stale-pattern detection alone to
	// decide whether the hook file is gc-generated vs user-authored.
	if hookExists &&
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

// readClaudeSettingsCandidate reads a candidate settings file. When strict is
// true, a file that exists-but-can't-be-read surfaces as an error so callers
// can fail loudly. When strict is false, unreadable files are reported as
// "not found" so a corrupt leftover file doesn't block fallback to the next
// source or to embedded defaults.
func readClaudeSettingsCandidate(fs fsys.FS, path string, strict bool) (string, []byte, bool, error) {
	data, err := fs.ReadFile(path)
	if err == nil {
		return path, data, true, nil
	}
	if strict {
		if _, statErr := fs.Stat(path); statErr == nil {
			return "", nil, false, fmt.Errorf("reading %s: %w", path, err)
		}
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
	// The pattern uses JSON-escaped quotes to match how the string appears
	// in the embedded file bytes. Without the escapes, strings.Replace
	// finds nothing and stale == current — which silently flags every
	// base-equal file as "needs upgrade" and masks any precedence logic
	// that depends on this predicate.
	stale := strings.Replace(string(current), `gc handoff \"context cycle\"`, `gc prime --hook`, 1)
	return string(existing) == stale
}
