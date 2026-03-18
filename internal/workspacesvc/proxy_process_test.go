package workspacesvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/supervisor"
)

func TestManagerReloadProxyProcessStartsAndProxies(t *testing.T) {
	t.Setenv("GC_SERVICE_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}

	rt := &testRuntime{
		cityPath: t.TempDir(),
		cityName: "test-city",
		cfg: &config.City{
			Services: []config.Service{{
				Name: "bridge",
				Kind: "proxy_process",
				Process: config.ServiceProcessConfig{
					Command:    []string{exe, "-test.run=^TestProxyProcessHelper$", "--"},
					HealthPath: "/healthz",
				},
			}},
		},
		sp:    runtime.NewFake(),
		store: beads.NewMemStore(),
	}

	mgr := NewManager(rt)
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	defer mgr.Close() //nolint:errcheck // best-effort cleanup

	status, ok := mgr.Get("bridge")
	if !ok {
		t.Fatal("service status missing")
	}
	if status.LocalState != "ready" {
		logData, _ := os.ReadFile(filepath.Join(rt.cityPath, ".gc", "services", "bridge", "logs", "service.log"))
		t.Fatalf("LocalState = %q, want ready (reason=%q, log=%q)", status.LocalState, status.Reason, string(logData))
	}

	req := httptest.NewRequest(http.MethodPost, "/svc/bridge/hooks/example", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	if ok := mgr.ServeHTTP(rec, req); !ok {
		t.Fatal("ServeHTTP returned false, want true")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if strings.TrimSpace(rec.Body.String()) != "POST /hooks/example" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "POST /hooks/example")
	}
}

func TestProxyProcessHelper(t *testing.T) {
	if os.Getenv("GC_SERVICE_HELPER") != "1" {
		t.Skip("helper process")
	}
	socketPath := os.Getenv("GC_SERVICE_SOCKET")
	if socketPath == "" {
		t.Fatal("GC_SERVICE_SOCKET not set")
	}
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close() //nolint:errcheck // best-effort cleanup

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/env", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"GC_CITY_ROOT":              os.Getenv("GC_CITY_ROOT"),
			"GC_CITY_RUNTIME_DIR":       os.Getenv("GC_CITY_RUNTIME_DIR"),
			"GC_SERVICE_NAME":           os.Getenv("GC_SERVICE_NAME"),
			"GC_SERVICE_STATE_ROOT":     os.Getenv("GC_SERVICE_STATE_ROOT"),
			"GC_SERVICE_PUBLIC_URL":     os.Getenv("GC_SERVICE_PUBLIC_URL"),
			"GC_SERVICE_VISIBILITY":     os.Getenv("GC_SERVICE_VISIBILITY"),
			"GC_PUBLISHED_SERVICES_DIR": os.Getenv("GC_PUBLISHED_SERVICES_DIR"),
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", r.Method, r.URL.Path) //nolint:errcheck // test helper
	})

	srv := &http.Server{Handler: mux}
	err = srv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve: %v", err)
	}
}

func TestProxyProcessPublishesServiceEnv(t *testing.T) {
	t.Setenv("GC_SERVICE_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}

	rt := &testRuntime{
		cityPath: t.TempDir(),
		cityName: "test-city",
		cfg: &config.City{
			Workspace: config.Workspace{Name: "demo-app"},
			Services: []config.Service{{
				Name: "bridge",
				Kind: "proxy_process",
				Publication: config.ServicePublicationConfig{
					Visibility: "public",
				},
				Process: config.ServiceProcessConfig{
					Command:    []string{exe, "-test.run=^TestProxyProcessHelper$", "--"},
					HealthPath: "/healthz",
				},
			}},
		},
		pubCfg: supervisor.PublicationConfig{
			Provider:         "hosted",
			TenantSlug:       "acme",
			PublicBaseDomain: "apps.example.com",
		},
		sp:    runtime.NewFake(),
		store: beads.NewMemStore(),
	}

	mgr := NewManager(rt)
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	defer mgr.Close() //nolint:errcheck // best-effort cleanup

	req := httptest.NewRequest(http.MethodGet, "/svc/bridge/env", nil)
	rec := httptest.NewRecorder()
	if ok := mgr.ServeHTTP(rec, req); !ok {
		t.Fatal("ServeHTTP returned false, want true")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var env map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode env: %v", err)
	}
	if env["GC_CITY_ROOT"] != rt.cityPath {
		t.Fatalf("GC_CITY_ROOT = %q, want %q", env["GC_CITY_ROOT"], rt.cityPath)
	}
	if env["GC_CITY_RUNTIME_DIR"] != filepath.Join(rt.cityPath, ".gc", "runtime") {
		t.Fatalf("GC_CITY_RUNTIME_DIR = %q, want %q", env["GC_CITY_RUNTIME_DIR"], filepath.Join(rt.cityPath, ".gc", "runtime"))
	}
	if env["GC_SERVICE_NAME"] != "bridge" {
		t.Fatalf("GC_SERVICE_NAME = %q, want bridge", env["GC_SERVICE_NAME"])
	}
	if env["GC_SERVICE_STATE_ROOT"] != filepath.Join(rt.cityPath, ".gc", "services", "bridge") {
		t.Fatalf("GC_SERVICE_STATE_ROOT = %q, want %q", env["GC_SERVICE_STATE_ROOT"], filepath.Join(rt.cityPath, ".gc", "services", "bridge"))
	}
	if !strings.HasPrefix(env["GC_SERVICE_PUBLIC_URL"], "https://bridge--demo-app--acme--") {
		t.Fatalf("GC_SERVICE_PUBLIC_URL = %q, want bridge--demo-app--acme prefix", env["GC_SERVICE_PUBLIC_URL"])
	}
	if env["GC_SERVICE_VISIBILITY"] != "public" {
		t.Fatalf("GC_SERVICE_VISIBILITY = %q, want public", env["GC_SERVICE_VISIBILITY"])
	}
	if env["GC_PUBLISHED_SERVICES_DIR"] != citylayout.PublishedServicesDir(rt.cityPath) {
		t.Fatalf("GC_PUBLISHED_SERVICES_DIR = %q, want %q", env["GC_PUBLISHED_SERVICES_DIR"], citylayout.PublishedServicesDir(rt.cityPath))
	}
}

func TestProxyProcessReloadRefreshesPublicationEnv(t *testing.T) {
	t.Setenv("GC_SERVICE_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}

	rt := &testRuntime{
		cityPath: t.TempDir(),
		cityName: "test-city",
		cfg: &config.City{
			Workspace: config.Workspace{Name: "demo-app"},
			Services: []config.Service{{
				Name: "bridge",
				Kind: "proxy_process",
				Publication: config.ServicePublicationConfig{
					Visibility: "public",
				},
				Process: config.ServiceProcessConfig{
					Command:    []string{exe, "-test.run=^TestProxyProcessHelper$", "--"},
					HealthPath: "/healthz",
				},
			}},
		},
		pubCfg: supervisor.PublicationConfig{
			Provider:         "hosted",
			TenantSlug:       "acme",
			PublicBaseDomain: "apps.example.com",
		},
		sp:    runtime.NewFake(),
		store: beads.NewMemStore(),
	}

	mgr := NewManager(rt)
	if err := mgr.Reload(); err != nil {
		t.Fatalf("first Reload: %v", err)
	}
	defer mgr.Close() //nolint:errcheck // best-effort cleanup

	loadEnv := func() map[string]string {
		req := httptest.NewRequest(http.MethodGet, "/svc/bridge/env", nil)
		rec := httptest.NewRecorder()
		if ok := mgr.ServeHTTP(rec, req); !ok {
			t.Fatal("ServeHTTP returned false, want true")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var env map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
			t.Fatalf("decode env: %v", err)
		}
		return env
	}

	first := loadEnv()
	rt.pubCfg.TenantSlug = "beta"
	if err := mgr.Reload(); err != nil {
		t.Fatalf("second Reload: %v", err)
	}
	second := loadEnv()

	if first["GC_SERVICE_PUBLIC_URL"] == second["GC_SERVICE_PUBLIC_URL"] {
		t.Fatalf("GC_SERVICE_PUBLIC_URL did not change across reload: %q", first["GC_SERVICE_PUBLIC_URL"])
	}
	if !strings.Contains(second["GC_SERVICE_PUBLIC_URL"], "--beta--") {
		t.Fatalf("GC_SERVICE_PUBLIC_URL = %q, want beta route", second["GC_SERVICE_PUBLIC_URL"])
	}
}
