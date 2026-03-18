package api

import (
	"net/http"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// providerCreateRequest is the JSON body for POST /v0/providers.
type providerCreateRequest struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	PromptMode   string            `json:"prompt_mode,omitempty"`
	PromptFlag   string            `json:"prompt_flag,omitempty"`
	ReadyDelayMs int               `json:"ready_delay_ms,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

// providerUpdateRequest is the JSON body for PATCH /v0/provider/{name}.
type providerUpdateRequest struct {
	DisplayName  *string           `json:"display_name,omitempty"`
	Command      *string           `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	PromptMode   *string           `json:"prompt_mode,omitempty"`
	PromptFlag   *string           `json:"prompt_flag,omitempty"`
	ReadyDelayMs *int              `json:"ready_delay_ms,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

func (s *Server) handleProviderCreate(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	var body providerCreateRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "name is required")
		return
	}
	if body.Command == "" {
		writeError(w, http.StatusBadRequest, "invalid", "command is required")
		return
	}

	spec := config.ProviderSpec{
		DisplayName:  body.DisplayName,
		Command:      body.Command,
		Args:         body.Args,
		PromptMode:   body.PromptMode,
		PromptFlag:   body.PromptFlag,
		ReadyDelayMs: body.ReadyDelayMs,
		Env:          body.Env,
	}

	if err := sm.CreateProvider(body.Name, spec); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			writeError(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "provider": body.Name})
}

func (s *Server) handleProviderUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	var body providerUpdateRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	patch := ProviderUpdate(body)

	if err := sm.UpdateProvider(name, patch); err != nil {
		if strings.Contains(err.Error(), "not found") {
			if isBuiltinProvider(name) {
				writeError(w, http.StatusConflict, "conflict",
					"provider "+name+" is a builtin; use PUT /v0/patches/providers to override")
				return
			}
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "provider": name})
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	if err := sm.DeleteProvider(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			if isBuiltinProvider(name) {
				writeError(w, http.StatusConflict, "conflict",
					"provider "+name+" is a builtin; use DELETE /v0/patches/provider/"+name+" to remove overrides")
				return
			}
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "provider": name})
}

// isBuiltinProvider checks if a name is a known builtin provider.
func isBuiltinProvider(name string) bool {
	_, ok := config.BuiltinProviders()[name]
	return ok
}
