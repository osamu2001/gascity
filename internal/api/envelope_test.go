package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, 200, map[string]string{"key": "value"})

	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var got map[string]string
	json.NewDecoder(rec.Body).Decode(&got) //nolint:errcheck
	if got["key"] != "value" {
		t.Errorf("body key = %q, want %q", got["key"], "value")
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, 404, "not_found", "bead not found")

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}

	var got Error
	json.NewDecoder(rec.Body).Decode(&got) //nolint:errcheck
	if got.Code != "not_found" {
		t.Errorf("code = %q, want %q", got.Code, "not_found")
	}
	if got.Message != "bead not found" {
		t.Errorf("message = %q, want %q", got.Message, "bead not found")
	}
}

func TestWriteListJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	items := []string{"a", "b"}
	writeListJSON(rec, 42, items, 2)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-GC-Index"); got != "42" {
		t.Errorf("X-GC-Index = %q, want %q", got, "42")
	}

	var resp listResponse
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp.Total != 2 {
		t.Errorf("Total = %d, want 2", resp.Total)
	}
}
