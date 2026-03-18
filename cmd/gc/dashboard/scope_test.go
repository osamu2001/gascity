package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScopedPath(t *testing.T) {
	tests := []struct {
		path      string
		cityScope string
		want      string
	}{
		// Standalone mode — no rewriting.
		{"/v0/sessions", "", "/v0/sessions"},
		{"/v0/events/stream", "", "/v0/events/stream"},
		{"/v0/bead/abc123", "", "/v0/bead/abc123"},
		{"/health", "", "/health"},

		// Supervisor mode — /v0/ paths get city prefix.
		{"/v0/sessions", "bright-lights", "/v0/city/bright-lights/sessions"},
		{"/v0/events/stream", "bright-lights", "/v0/city/bright-lights/events/stream"},
		{"/v0/bead/abc123", "bright-lights", "/v0/city/bright-lights/bead/abc123"},
		{"/v0/session/abc123/transcript", "mytown", "/v0/city/mytown/session/abc123/transcript"},
		{"/v0/beads?status=open&limit=50", "mytown", "/v0/city/mytown/beads?status=open&limit=50"},

		// Non-/v0/ paths are never rewritten.
		{"/health", "bright-lights", "/health"},
		{"", "bright-lights", ""},
	}

	for _, tt := range tests {
		got := scopedPath(tt.path, tt.cityScope)
		if got != tt.want {
			t.Errorf("scopedPath(%q, %q) = %q, want %q", tt.path, tt.cityScope, got, tt.want)
		}
	}
}

func TestDetectSupervisor(t *testing.T) {
	t.Run("supervisor with cities", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v0/cities" {
				http.NotFound(w, r)
				return
			}
			resp := struct {
				Items []struct {
					Name string `json:"name"`
				} `json:"items"`
				Total int `json:"total"`
			}{
				Items: []struct {
					Name string `json:"name"`
				}{
					{Name: "bright-lights"},
					{Name: "test-city"},
				},
				Total: 2,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
		}))
		defer srv.Close()

		if !detectSupervisor(srv.URL) {
			t.Error("detectSupervisor() = false, want true")
		}
	})

	t.Run("standalone mode (404)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		if detectSupervisor(srv.URL) {
			t.Error("detectSupervisor() = true, want false")
		}
	})

	t.Run("unreachable server", func(t *testing.T) {
		if detectSupervisor("http://127.0.0.1:1") {
			t.Error("detectSupervisor() = true, want false")
		}
	})
}

func TestFetchCityTabs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/cities" {
			http.NotFound(w, r)
			return
		}
		resp := struct {
			Items []struct {
				Name    string `json:"name"`
				Running bool   `json:"running"`
			} `json:"items"`
			Total int `json:"total"`
		}{
			Items: []struct {
				Name    string `json:"name"`
				Running bool   `json:"running"`
			}{
				{Name: "bright-lights", Running: true},
				{Name: "stopped-city", Running: false},
			},
			Total: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	tabs := fetchCityTabs(srv.URL)
	if len(tabs) != 2 {
		t.Fatalf("fetchCityTabs() returned %d tabs, want 2", len(tabs))
	}
	if tabs[0].Name != "bright-lights" || !tabs[0].Running {
		t.Errorf("tabs[0] = %+v, want {bright-lights, true}", tabs[0])
	}
	if tabs[1].Name != "stopped-city" || tabs[1].Running {
		t.Errorf("tabs[1] = %+v, want {stopped-city, false}", tabs[1])
	}
}

func TestAPIFetcherWithScope(t *testing.T) {
	f := NewAPIFetcher("http://example.com", "/tmp/city", "mytown")
	if f.cityScope != "" {
		t.Errorf("new fetcher cityScope = %q, want empty", f.cityScope)
	}

	scoped := f.WithScope("bright-lights")
	if scoped.cityScope != "bright-lights" {
		t.Errorf("scoped cityScope = %q, want %q", scoped.cityScope, "bright-lights")
	}
	// Original unchanged.
	if f.cityScope != "" {
		t.Errorf("original cityScope changed to %q, want empty", f.cityScope)
	}
	// Shared client.
	if scoped.client != f.client {
		t.Error("scoped fetcher should share the HTTP client")
	}
}
