package dashboard

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchServices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/services" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"service_name":"healthz","kind":"workflow","mount_path":"/svc/healthz","publish_mode":"private","url":"http://127.0.0.1:9443/svc/healthz","state":"ready","local_state":"ready","publication_state":"private"}],"total":1}`))
	}))
	defer srv.Close()

	fetcher := NewAPIFetcher(srv.URL, "/tmp/city", "test-city")
	rows, err := fetcher.FetchServices()
	if err != nil {
		t.Fatalf("FetchServices: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Name != "healthz" {
		t.Fatalf("Name = %q, want healthz", rows[0].Name)
	}
	if rows[0].State != "ready" {
		t.Fatalf("ServiceState = %q, want ready", rows[0].State)
	}
}

func TestFetchServicesEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/services" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	fetcher := NewAPIFetcher(srv.URL, "/tmp/city", "test-city")
	rows, err := fetcher.FetchServices()
	if err != nil {
		t.Fatalf("FetchServices: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if rows == nil {
		t.Fatal("rows = nil, want empty slice")
	}
}

func TestFetchServicesMissingEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	fetcher := NewAPIFetcher(srv.URL, "/tmp/city", "test-city")
	rows, err := fetcher.FetchServices()
	var unavailable servicesUnavailable
	if !errors.As(err, &unavailable) || !unavailable.ServicesUnavailable() {
		t.Fatalf("FetchServices err = %v, want services-unavailable error", err)
	}
	if rows != nil {
		t.Fatalf("rows = %#v, want nil", rows)
	}
}

func TestTemplateRendersServicesPanel(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", ConvoyData{
		Services: []ServiceRow{{
			Name:       "healthz",
			Kind:       "workflow",
			State:      "ready",
			LocalState: "ready",
		}},
		CSRFToken: "csrf-token",
	})
	if err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	body := buf.String()
	for _, want := range []string{"🛰️ Services", "healthz", "ready"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q:\n%s", want, body)
		}
	}
}

func TestTemplateRendersUnavailableServicesPanel(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", ConvoyData{
		ServicesState: servicePanelStateUnavailable,
		CSRFToken:     "csrf-token",
	})
	if err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Workspace services unavailable on this city API") {
		t.Fatalf("response missing unavailable state:\n%s", body)
	}
	if !strings.Contains(body, "<span class=\"count\">n/a</span>") {
		t.Fatalf("response missing unavailable count badge:\n%s", body)
	}
}
