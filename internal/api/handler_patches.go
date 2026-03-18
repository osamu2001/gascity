package api

import (
	"net/http"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// --- Agent patches ---

func (s *Server) handleAgentPatchList(w http.ResponseWriter, _ *http.Request) {
	cfg := s.state.Config()
	patches := cfg.Patches.Agents
	if patches == nil {
		patches = []config.AgentPatch{}
	}
	writeListJSON(w, s.latestIndex(), patches, len(patches))
}

func (s *Server) handleAgentPatchGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.state.Config()
	dir, base := config.ParseQualifiedName(name)
	for _, p := range cfg.Patches.Agents {
		if p.Dir == dir && p.Name == base {
			writeIndexJSON(w, s.latestIndex(), p)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "agent patch "+name+" not found")
}

func (s *Server) handleAgentPatchSet(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	var patch config.AgentPatch
	if err := decodeBody(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if patch.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "name is required")
		return
	}

	if err := sm.SetAgentPatch(patch); err != nil {
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	qn := patch.Name
	if patch.Dir != "" {
		qn = patch.Dir + "/" + patch.Name
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "agent_patch": qn})
}

func (s *Server) handleAgentPatchDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	if err := sm.DeleteAgentPatch(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "agent_patch": name})
}

// --- Rig patches ---

func (s *Server) handleRigPatchList(w http.ResponseWriter, _ *http.Request) {
	cfg := s.state.Config()
	patches := cfg.Patches.Rigs
	if patches == nil {
		patches = []config.RigPatch{}
	}
	writeListJSON(w, s.latestIndex(), patches, len(patches))
}

func (s *Server) handleRigPatchGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.state.Config()
	for _, p := range cfg.Patches.Rigs {
		if p.Name == name {
			writeIndexJSON(w, s.latestIndex(), p)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "rig patch "+name+" not found")
}

func (s *Server) handleRigPatchSet(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	var patch config.RigPatch
	if err := decodeBody(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if patch.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "name is required")
		return
	}

	if err := sm.SetRigPatch(patch); err != nil {
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "rig_patch": patch.Name})
}

func (s *Server) handleRigPatchDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	if err := sm.DeleteRigPatch(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "rig_patch": name})
}

// --- Provider patches ---

func (s *Server) handleProviderPatchList(w http.ResponseWriter, _ *http.Request) {
	cfg := s.state.Config()
	patches := cfg.Patches.Providers
	if patches == nil {
		patches = []config.ProviderPatch{}
	}
	writeListJSON(w, s.latestIndex(), patches, len(patches))
}

func (s *Server) handleProviderPatchGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.state.Config()
	for _, p := range cfg.Patches.Providers {
		if p.Name == name {
			writeIndexJSON(w, s.latestIndex(), p)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "provider patch "+name+" not found")
}

func (s *Server) handleProviderPatchSet(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	var patch config.ProviderPatch
	if err := decodeBody(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if patch.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "name is required")
		return
	}

	if err := sm.SetProviderPatch(patch); err != nil {
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "provider_patch": patch.Name})
}

func (s *Server) handleProviderPatchDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	if err := sm.DeleteProviderPatch(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "provider_patch": name})
}
