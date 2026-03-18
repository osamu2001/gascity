package workspacesvc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/config"
)

const (
	proxyProcessReadyTimeout   = 5 * time.Second
	proxyProcessRestartBackoff = 1 * time.Second
	proxyProcessShutdownWait   = 2 * time.Second
)

var errProxyProcessExitedEarly = errors.New("process exited before listener became ready")

type proxyProcessInstance struct {
	rt           RuntimeContext
	svc          config.Service
	absStateRoot string
	socketPath   string
	healthPath   string
	transport    *http.Transport

	mu          sync.Mutex
	cmd         *exec.Cmd
	doneCh      chan struct{}
	nextRestart time.Time
	closed      bool
	status      Status
}

func newProxyProcessInstance(rt RuntimeContext, svc config.Service) (Instance, error) {
	absRoot := svc.StateRootOrDefault()
	if !filepath.IsAbs(absRoot) {
		absRoot = filepath.Join(rt.CityPath(), absRoot)
	}
	socketPath := filepath.Join(rt.CityPath(), ".gc", "run", "services", svc.Name+".sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	inst := &proxyProcessInstance{
		rt:           rt,
		svc:          svc,
		absStateRoot: absRoot,
		socketPath:   socketPath,
		healthPath:   svc.Process.HealthPath,
		transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		status: Status{
			ServiceName: svc.Name,
			Kind:        svc.KindOrDefault(),
			State:       "starting",
			LocalState:  "starting",
			UpdatedAt:   time.Now().UTC(),
		},
	}
	if err := inst.start(time.Now().UTC()); err != nil {
		return nil, err
	}
	return inst, nil
}

func (p *proxyProcessInstance) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.status
	out.UpdatedAt = time.Now().UTC()
	return out
}

func (p *proxyProcessInstance) HandleHTTP(w http.ResponseWriter, r *http.Request, subpath string) bool {
	p.mu.Lock()
	ready := !p.closed && p.cmd != nil && p.status.LocalState == "ready"
	reason := p.status.Reason
	transport := p.transport
	p.mu.Unlock()

	if !ready {
		http.Error(w, serviceUnavailableMessage(reason), http.StatusServiceUnavailable)
		return true
	}

	target := &url.URL{Scheme: "http", Host: "gc-service"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, fmt.Sprintf("service unavailable: %v", err), http.StatusBadGateway)
	}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = subpath
		req.URL.RawPath = subpath
		req.Host = ""
	}
	proxy.ServeHTTP(w, r)
	return true
}

func (p *proxyProcessInstance) Tick(_ context.Context, now time.Time) {
	p.mu.Lock()
	closed := p.closed
	shouldStart := p.cmd == nil && (p.nextRestart.IsZero() || !now.Before(p.nextRestart))
	p.mu.Unlock()
	if closed {
		return
	}
	if shouldStart {
		if err := p.start(now); err != nil {
			p.mu.Lock()
			p.status.State = "degraded"
			p.status.LocalState = "degraded"
			p.status.Reason = err.Error()
			p.status.UpdatedAt = now
			p.nextRestart = now.Add(proxyProcessRestartBackoff)
			p.mu.Unlock()
		}
		return
	}
	if err := p.checkHealth(now); err != nil {
		p.mu.Lock()
		if !p.closed && p.cmd != nil {
			p.status.State = "degraded"
			p.status.LocalState = "degraded"
			p.status.Reason = err.Error()
			p.status.UpdatedAt = now
		}
		p.mu.Unlock()
	}
}

func (p *proxyProcessInstance) Close() error {
	p.mu.Lock()
	p.closed = true
	cmd := p.cmd
	p.cmd = nil
	p.status.State = "stopped"
	p.status.LocalState = "stopped"
	p.status.Reason = "service_closed"
	p.status.UpdatedAt = time.Now().UTC()
	p.mu.Unlock()

	if cmd != nil {
		return stopProcessGroup(cmd)
	}
	return nil
}

func (p *proxyProcessInstance) start(now time.Time) error {
	logFile, err := os.OpenFile(filepath.Join(p.absStateRoot, "logs", "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open service log: %w", err)
	}
	_ = os.Remove(p.socketPath)
	status := baseStatus(p.rt.Config(), p.rt.PublicationConfig(), p.svc, now)

	cmd := exec.Command(p.svc.Process.Command[0], p.svc.Process.Command[1:]...)
	cmd.Dir = p.commandDir()
	cmd.Env = append(os.Environ(), citylayout.CityRuntimeEnv(p.rt.CityPath())...)
	cmd.Env = append(cmd.Env,
		"GC_SERVICE_NAME="+p.svc.Name,
		"GC_SERVICE_STATE_ROOT="+p.absStateRoot,
		"GC_SERVICE_RUN_ROOT="+filepath.Join(p.absStateRoot, "run"),
		"GC_SERVICE_SOCKET="+p.socketPath,
		"GC_SERVICE_URL_PREFIX="+p.svc.MountPathOrDefault(),
		"GC_SERVICE_PUBLIC_URL="+status.URL,
		"GC_SERVICE_VISIBILITY="+status.Visibility,
		"GC_PUBLISHED_SERVICES_DIR="+citylayout.PublishedServicesDir(p.rt.CityPath()),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start process: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	doneCh := make(chan struct{})
	p.doneCh = doneCh
	p.nextRestart = time.Time{}
	p.status.State = "starting"
	p.status.LocalState = "starting"
	p.status.Reason = ""
	p.status.UpdatedAt = now
	p.mu.Unlock()

	go func(cmd *exec.Cmd, logFile *os.File, doneCh chan struct{}) {
		err := cmd.Wait()
		_ = logFile.Close()

		p.mu.Lock()
		defer close(doneCh)
		defer p.mu.Unlock()
		if p.cmd != cmd {
			return
		}
		p.cmd = nil
		if p.closed {
			return
		}
		p.status.State = "degraded"
		p.status.LocalState = "degraded"
		p.status.Reason = processExitReason(err)
		p.status.UpdatedAt = time.Now().UTC()
		p.nextRestart = time.Now().UTC().Add(proxyProcessRestartBackoff)
	}(cmd, logFile, doneCh)

	if err := p.waitReady(now.Add(proxyProcessReadyTimeout)); err != nil {
		if !errors.Is(err, errProxyProcessExitedEarly) {
			_ = stopProcessGroup(cmd)
		}
		return err
	}

	p.mu.Lock()
	if p.cmd == cmd && !p.closed {
		p.status.State = "ready"
		p.status.LocalState = "ready"
		p.status.Reason = ""
		p.status.UpdatedAt = time.Now().UTC()
	}
	p.mu.Unlock()
	return nil
}

func (p *proxyProcessInstance) waitReady(deadline time.Time) error {
	for time.Now().Before(deadline) {
		p.mu.Lock()
		closed := p.closed
		doneCh := p.doneCh
		p.mu.Unlock()
		if closed {
			return errors.New("service closed")
		}
		select {
		case <-doneCh:
			return errProxyProcessExitedEarly
		default:
		}
		if conn, err := net.DialTimeout("unix", p.socketPath, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			if p.healthPath == "" {
				return nil
			}
			if err := p.checkHealth(time.Now().UTC()); err == nil {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("service %q did not become ready before timeout", p.svc.Name)
}

func (p *proxyProcessInstance) checkHealth(now time.Time) error {
	if p.healthPath == "" {
		p.mu.Lock()
		if p.cmd != nil && !p.closed {
			p.status.State = "ready"
			p.status.LocalState = "ready"
			p.status.Reason = ""
			p.status.UpdatedAt = now
		}
		p.mu.Unlock()
		return nil
	}

	client := &http.Client{
		Timeout:   500 * time.Millisecond,
		Transport: p.transport,
	}
	req, err := http.NewRequest(http.MethodGet, "http://gc-service"+p.healthPath, nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	p.mu.Lock()
	if p.cmd != nil && !p.closed {
		p.status.State = "ready"
		p.status.LocalState = "ready"
		p.status.Reason = ""
		p.status.UpdatedAt = now
	}
	p.mu.Unlock()
	return nil
}

func (p *proxyProcessInstance) commandDir() string {
	if p.svc.SourceDir != "" {
		return p.svc.SourceDir
	}
	return p.rt.CityPath()
}

func processExitReason(err error) string {
	if err == nil {
		return "process_exited"
	}
	return fmt.Sprintf("process exited: %v", err)
}

func serviceUnavailableMessage(reason string) string {
	if reason == "" {
		return "service unavailable"
	}
	return "service unavailable: " + reason
}

func stopProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	deadline := time.Now().Add(proxyProcessShutdownWait)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return nil
}
