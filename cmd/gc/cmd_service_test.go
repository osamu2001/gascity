package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/workspacesvc"
)

type fakeServiceReader struct {
	items []workspacesvc.Status
	get   map[string]workspacesvc.Status
	err   error
}

func (f fakeServiceReader) ListServices() ([]workspacesvc.Status, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f fakeServiceReader) GetService(name string) (workspacesvc.Status, error) {
	if f.err != nil {
		return workspacesvc.Status{}, f.err
	}
	status, ok := f.get[name]
	if !ok {
		return workspacesvc.Status{}, fmt.Errorf("missing service %s", name)
	}
	return status, nil
}

func TestDoServiceListUsesLiveStatuses(t *testing.T) {
	cfg := &config.City{
		Services: []config.Service{{
			Name:     "healthz",
			Workflow: config.ServiceWorkflowConfig{Contract: "gc.healthz.v1"},
		}},
	}
	reader := fakeServiceReader{
		items: []workspacesvc.Status{{
			ServiceName:      "healthz",
			Kind:             "workflow",
			MountPath:        "/svc/healthz",
			PublishMode:      "direct",
			State:            "ready",
			LocalState:       "ready",
			PublicationState: "direct",
			URL:              "http://127.0.0.1:9443/svc/healthz",
		}},
	}

	var stdout, stderr bytes.Buffer
	if code := doServiceList(cfg, reader, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "healthz") || !strings.Contains(out, "ready") || !strings.Contains(out, "http://127.0.0.1:9443/svc/healthz") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestDoServiceDoctorFallsBackToConfigView(t *testing.T) {
	cfg := &config.City{
		Services: []config.Service{{
			Name:        "review-intake",
			PublishMode: "private",
			Workflow:    config.ServiceWorkflowConfig{Contract: "pack.gc/review.v1"},
		}},
	}

	var stdout, stderr bytes.Buffer
	if code := doServiceDoctor(cfg, nil, "review-intake", &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Contract:          pack.gc/review.v1") {
		t.Fatalf("missing contract in output:\n%s", out)
	}
	if !strings.Contains(out, "Observed State:    controller API unavailable") {
		t.Fatalf("missing fallback note in output:\n%s", out)
	}
}

func TestDoServiceDoctorMissingService(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := doServiceDoctor(&config.City{}, nil, "missing", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `service "missing" not found`) {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}
