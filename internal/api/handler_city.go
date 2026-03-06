package api

import (
	"net/http"
	"strings"
)

// cityPatchRequest is the JSON body for PATCH /v0/city.
type cityPatchRequest struct {
	Suspended *bool `json:"suspended,omitempty"`
}

func (s *Server) handleCityPatch(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.state.(StateMutator)
	if !ok {
		writeError(w, http.StatusNotImplemented, "internal", "mutations not supported")
		return
	}

	var body cityPatchRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}

	if body.Suspended == nil {
		writeError(w, http.StatusBadRequest, "invalid", "no fields to update")
		return
	}

	var err error
	if *body.Suspended {
		err = sm.SuspendCity()
	} else {
		err = sm.ResumeCity()
	}
	if err != nil {
		if strings.Contains(err.Error(), "validating") {
			writeError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
