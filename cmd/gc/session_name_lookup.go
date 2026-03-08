package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// resolveSessionName returns the session name for a qualified agent name.
// When a bead store is available, it looks up an existing session bead and
// returns its session_name metadata. When no bead is found (or no store is
// available), it falls back to the legacy SessionNameFor function.
//
// Phase 1 is lookup-only — no beads are created here. Bead creation moves
// to Phase 2 when all consumers (CLI commands, prompt templates) are fully
// wired to the bead store.
//
// templateName is the base config template name (e.g., "worker" for pool
// instance "worker-1"). For non-pool agents, templateName == qualifiedName.
// Phase 1 ignores templateName (lookup uses qualifiedName only); Phase 2
// will use it for pool instance bead creation and template-based queries.
//
// Results are cached in p.beadNames for the duration of the build cycle.
func (p *agentBuildParams) resolveSessionName(qualifiedName, _ /* templateName */ string) string {
	// Check cache first.
	if sn, ok := p.beadNames[qualifiedName]; ok {
		return sn
	}

	// Try bead store lookup if available.
	if p.beadStore != nil {
		sn := findSessionNameByTemplate(p.beadStore, qualifiedName)
		if sn != "" {
			p.beadNames[qualifiedName] = sn
			return sn
		}
	}

	// No bead found (or no store) → legacy path.
	sn := agent.SessionNameFor(p.cityName, qualifiedName, p.sessionTemplate)
	p.beadNames[qualifiedName] = sn
	return sn
}

// sessionNameFromBeadID derives the tmux session name from a bead ID.
// This is the universal naming convention: "s-" + beadID with "/" replaced.
func sessionNameFromBeadID(beadID string) string {
	return "s-" + strings.ReplaceAll(beadID, "/", "--")
}

// findSessionNameByTemplate searches for an open session bead with the given
// template and returns its session_name metadata. Returns "" if not found.
// Pool instance beads (those with pool_slot metadata) are skipped to prevent
// a template query like "worker" from matching pool instance "worker-1".
func findSessionNameByTemplate(store beads.Store, template string) string {
	// Search both session bead types.
	for _, label := range []string{session.LabelSession, sessionBeadLabel} {
		all, err := store.ListByLabel(label, 0)
		if err != nil {
			continue
		}
		for _, b := range all {
			if b.Status == "closed" {
				continue
			}
			// Skip pool instance beads — they should only be matched
			// by their specific instance name, not the base template.
			if b.Metadata["pool_slot"] != "" {
				continue
			}
			if b.Metadata["template"] == template || b.Metadata["common_name"] == template {
				if sn := b.Metadata["session_name"]; sn != "" {
					return sn
				}
			}
		}
	}
	return ""
}

// lookupSessionName resolves a qualified agent name to its bead-derived
// session name by querying the bead store. Returns the session name and
// true if found, or ("", false) if no matching session bead exists.
//
// This is the CLI-facing equivalent of agentBuildParams.resolveSessionName,
// for use by commands that don't go through buildDesiredState.
func lookupSessionName(store beads.Store, qualifiedName string) (string, bool) {
	if store == nil {
		return "", false
	}
	sn := findSessionNameByTemplate(store, qualifiedName)
	if sn != "" {
		return sn, true
	}
	return "", false
}

// lookupSessionNameOrLegacy resolves a qualified agent name to its session
// name. Tries the bead store first; falls back to the legacy SessionNameFor
// function if no bead is found.
func lookupSessionNameOrLegacy(store beads.Store, cityName, qualifiedName, sessionTemplate string) string {
	if sn, ok := lookupSessionName(store, qualifiedName); ok {
		return sn
	}
	return agent.SessionNameFor(cityName, qualifiedName, sessionTemplate)
}
