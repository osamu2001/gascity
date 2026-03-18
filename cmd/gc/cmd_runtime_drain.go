package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/spf13/cobra"
)

// drainOps abstracts drain signal operations for testability.
type drainOps interface {
	setDrain(sessionName string) error
	clearDrain(sessionName string) error
	isDraining(sessionName string) (bool, error)
	drainStartTime(sessionName string) (time.Time, error)
	setDrainAck(sessionName string) error
	isDrainAcked(sessionName string) (bool, error)
	setRestartRequested(sessionName string) error
	isRestartRequested(sessionName string) (bool, error)
	clearRestartRequested(sessionName string) error
	setDriftRestart(sessionName string) error
	isDriftRestart(sessionName string) (bool, error)
	clearDriftRestart(sessionName string) error
}

// providerDrainOps implements drainOps using runtime.Provider metadata.
type providerDrainOps struct {
	sp runtime.Provider
}

func (o *providerDrainOps) setDrain(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_DRAIN", strconv.FormatInt(time.Now().Unix(), 10))
}

func (o *providerDrainOps) clearDrain(sessionName string) error {
	_ = o.sp.RemoveMeta(sessionName, "GC_DRAIN_ACK")
	return o.sp.RemoveMeta(sessionName, "GC_DRAIN")
}

func (o *providerDrainOps) isDraining(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRAIN")
	if err != nil {
		return false, nil // can't read = not draining
	}
	return val != "", nil
}

func (o *providerDrainOps) drainStartTime(sessionName string) (time.Time, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRAIN")
	if err != nil {
		return time.Time{}, fmt.Errorf("reading GC_DRAIN: %w", err)
	}
	if val == "" {
		return time.Time{}, fmt.Errorf("GC_DRAIN not set")
	}
	unix, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing GC_DRAIN timestamp %q: %w", val, err)
	}
	return time.Unix(unix, 0), nil
}

func (o *providerDrainOps) setDrainAck(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_DRAIN_ACK", "1")
}

func (o *providerDrainOps) isDrainAcked(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRAIN_ACK")
	if err != nil {
		return false, nil
	}
	return val == "1", nil
}

func (o *providerDrainOps) setRestartRequested(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_RESTART_REQUESTED", strconv.FormatInt(time.Now().Unix(), 10))
}

func (o *providerDrainOps) isRestartRequested(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_RESTART_REQUESTED")
	if err != nil {
		return false, nil
	}
	return val != "", nil
}

func (o *providerDrainOps) clearRestartRequested(sessionName string) error {
	return o.sp.RemoveMeta(sessionName, "GC_RESTART_REQUESTED")
}

func (o *providerDrainOps) setDriftRestart(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_DRIFT_RESTART", "1")
}

func (o *providerDrainOps) isDriftRestart(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRIFT_RESTART")
	if err != nil {
		return false, nil
	}
	return val == "1", nil
}

func (o *providerDrainOps) clearDriftRestart(sessionName string) error {
	return o.sp.RemoveMeta(sessionName, "GC_DRIFT_RESTART")
}

// newDrainOps creates a drainOps from a runtime.Provider.
func newDrainOps(sp runtime.Provider) drainOps {
	return &providerDrainOps{sp: sp}
}

// ---------------------------------------------------------------------------
// gc runtime drain <name>
// ---------------------------------------------------------------------------

func newRuntimeDrainCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "drain <name>",
		Short: "Signal an agent to drain (wind down gracefully)",
		Long: `Signal an agent to drain — wind down its current work gracefully.

Sets a GC_DRAIN metadata flag on the session. The agent should check
for drain status periodically (via "gc runtime drain-check") and finish
its current task before exiting. Use "gc runtime undrain" to cancel.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeDrain(args, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
}

func cmdRuntimeDrain(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "gc runtime drain: missing agent name") //nolint:errcheck // best-effort stderr
		return 1
	}
	agentName := args[0]

	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	found, ok := resolveAgentIdentity(cfg, agentName, currentRigContext(cfg))
	if !ok {
		fmt.Fprintln(stderr, agentNotFoundMsg("gc runtime drain", agentName, cfg)) //nolint:errcheck // best-effort stderr
		return 1
	}
	agentName = found.QualifiedName()

	cityName := cfg.Workspace.Name
	if cityName == "" {
		cityName = filepath.Base(cityPath)
	}
	sn := cliSessionName(cityPath, cityName, agentName, cfg.Workspace.SessionTemplate)
	sp := newSessionProvider()
	dops := newDrainOps(sp)
	rec := openCityRecorder(stderr)
	return doRuntimeDrain(dops, sp, rec, agentName, sn, stdout, stderr)
}

// doRuntimeDrain sets the drain signal on an agent's session.
func doRuntimeDrain(dops drainOps, sp runtime.Provider, rec events.Recorder,
	agentName, sn string, stdout, stderr io.Writer,
) int {
	if !sp.IsRunning(sn) {
		fmt.Fprintf(stderr, "gc runtime drain: agent %q is not running\n", agentName) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := dops.setDrain(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	rec.Record(events.Event{
		Type:    events.SessionDraining,
		Actor:   eventActor(),
		Subject: agentName,
	})
	fmt.Fprintf(stdout, "Draining agent '%s'\n", agentName) //nolint:errcheck // best-effort stdout
	return 0
}

// ---------------------------------------------------------------------------
// gc runtime undrain <name>
// ---------------------------------------------------------------------------

func newRuntimeUndrainCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "undrain <name>",
		Short: "Cancel drain on an agent",
		Long: `Cancel a pending drain signal on an agent.

Clears the GC_DRAIN and GC_DRAIN_ACK metadata flags, allowing the
agent to continue normal operation.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeUndrain(args, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
}

func cmdRuntimeUndrain(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "gc runtime undrain: missing agent name") //nolint:errcheck // best-effort stderr
		return 1
	}
	agentName := args[0]

	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	found, ok := resolveAgentIdentity(cfg, agentName, currentRigContext(cfg))
	if !ok {
		fmt.Fprintln(stderr, agentNotFoundMsg("gc runtime undrain", agentName, cfg)) //nolint:errcheck // best-effort stderr
		return 1
	}
	agentName = found.QualifiedName()

	cityName := cfg.Workspace.Name
	if cityName == "" {
		cityName = filepath.Base(cityPath)
	}
	sn := cliSessionName(cityPath, cityName, agentName, cfg.Workspace.SessionTemplate)
	sp := newSessionProvider()
	dops := newDrainOps(sp)
	rec := openCityRecorder(stderr)
	return doRuntimeUndrain(dops, sp, rec, agentName, sn, stdout, stderr)
}

// doRuntimeUndrain clears the drain signal on an agent's session.
func doRuntimeUndrain(dops drainOps, sp runtime.Provider, rec events.Recorder,
	agentName, sn string, stdout, stderr io.Writer,
) int {
	if !sp.IsRunning(sn) {
		fmt.Fprintf(stderr, "gc runtime undrain: agent %q is not running\n", agentName) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := dops.clearDrain(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	rec.Record(events.Event{
		Type:    events.SessionUndrained,
		Actor:   eventActor(),
		Subject: agentName,
	})
	fmt.Fprintf(stdout, "Undrained agent '%s'\n", agentName) //nolint:errcheck // best-effort stdout
	return 0
}

// ---------------------------------------------------------------------------
// gc runtime drain-check
// ---------------------------------------------------------------------------

func newRuntimeDrainCheckCmd(stdout, stderr io.Writer) *cobra.Command {
	_ = stdout // drain-check is silent on stdout
	return &cobra.Command{
		Use:   "drain-check [name]",
		Short: "Check if this agent is draining (exit 0 = draining)",
		Long: `Check if this agent is currently draining.

Returns exit code 0 if draining, 1 if not. Designed for use in
conditionals: "if gc runtime drain-check; then finish-up; fi".
Uses $GC_AGENT and $GC_CITY env vars when called without arguments.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeDrainCheck(args, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
}

func cmdRuntimeDrainCheck(args []string, stderr io.Writer) int {
	var agentName, cityDir string
	if len(args) > 0 {
		// Explicit: resolve via city config (same as gc runtime drain).
		agentName = args[0]
		var err error
		cityDir, err = resolveCity()
		if err != nil {
			return 1 // silent — same as current "not draining" behavior
		}
		cfg, err := loadCityConfig(cityDir)
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-check: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		found, ok := resolveAgentIdentity(cfg, agentName, currentRigContext(cfg))
		if !ok {
			fmt.Fprintln(stderr, agentNotFoundMsg("gc runtime drain-check", agentName, cfg)) //nolint:errcheck // best-effort stderr
			return 1
		}
		agentName = found.QualifiedName()
		cityName := cfg.Workspace.Name
		if cityName == "" {
			cityName = filepath.Base(cityDir)
		}
		sn := cliSessionName(cityDir, cityName, agentName, cfg.Workspace.SessionTemplate)
		sp := newSessionProvider()
		dops := newDrainOps(sp)
		return doRuntimeDrainCheck(dops, sn)
	}

	// Implicit: env vars (backward compat for agent sessions).
	agentName = os.Getenv("GC_AGENT")
	cityDir = os.Getenv("GC_CITY")
	if agentName == "" || cityDir == "" {
		return 1 // not in agent context → not draining
	}

	cfg, err := loadCityConfig(cityDir)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-check: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cityName := cfg.Workspace.Name
	if cityName == "" {
		cityName = filepath.Base(cityDir)
	}
	sn := cliSessionName(cityDir, cityName, agentName, cfg.Workspace.SessionTemplate)
	sp := newSessionProvider()
	dops := newDrainOps(sp)
	return doRuntimeDrainCheck(dops, sn)
}

// doRuntimeDrainCheck returns 0 if the agent is draining, 1 otherwise.
// Silent on stdout — designed for `if gc runtime drain-check; then ...`.
func doRuntimeDrainCheck(dops drainOps, sn string) int {
	draining, err := dops.isDraining(sn)
	if err != nil || !draining {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// gc runtime drain-ack
// ---------------------------------------------------------------------------

func newRuntimeDrainAckCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "drain-ack [name]",
		Short: "Acknowledge drain — signal the controller to stop this session",
		Long: `Acknowledge a drain signal — tell the controller to stop this session.

Sets GC_DRAIN_ACK metadata on the session. The controller will stop
the session on its next reconcile tick. Call this after the agent has
finished its current work in response to a drain signal.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeDrainAck(args, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
}

func cmdRuntimeDrainAck(args []string, stdout, stderr io.Writer) int {
	var agentName, cityDir string
	if len(args) > 0 {
		// Explicit: resolve via city config (same as gc runtime drain).
		agentName = args[0]
		var err error
		cityDir, err = resolveCity()
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		cfg, err := loadCityConfig(cityDir)
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		found, ok := resolveAgentIdentity(cfg, agentName, currentRigContext(cfg))
		if !ok {
			fmt.Fprintln(stderr, agentNotFoundMsg("gc runtime drain-ack", agentName, cfg)) //nolint:errcheck // best-effort stderr
			return 1
		}
		agentName = found.QualifiedName()
		cityName := cfg.Workspace.Name
		if cityName == "" {
			cityName = filepath.Base(cityDir)
		}
		sn := cliSessionName(cityDir, cityName, agentName, cfg.Workspace.SessionTemplate)
		sp := newSessionProvider()
		dops := newDrainOps(sp)
		return doRuntimeDrainAck(dops, sn, stdout, stderr)
	}

	// Implicit: env vars (backward compat for agent sessions).
	agentName = os.Getenv("GC_AGENT")
	cityDir = os.Getenv("GC_CITY")
	if agentName == "" || cityDir == "" {
		fmt.Fprintln(stderr, "gc runtime drain-ack: not in agent context (GC_AGENT/GC_CITY not set)") //nolint:errcheck // best-effort stderr
		return 1
	}

	cfg, err := loadCityConfig(cityDir)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cityName := cfg.Workspace.Name
	if cityName == "" {
		cityName = filepath.Base(cityDir)
	}
	sn := cliSessionName(cityDir, cityName, agentName, cfg.Workspace.SessionTemplate)
	sp := newSessionProvider()
	dops := newDrainOps(sp)
	return doRuntimeDrainAck(dops, sn, stdout, stderr)
}

// ---------------------------------------------------------------------------
// gc runtime request-restart
// ---------------------------------------------------------------------------

func newRuntimeRequestRestartCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "request-restart",
		Short: "Request controller restart this session (blocks until killed)",
		Long: `Signal the controller to stop and restart this agent session.

Sets GC_RESTART_REQUESTED metadata on the session, then blocks forever.
The controller will stop the session on its next reconcile tick and
restart it fresh. The blocking prevents the agent from consuming more
context while waiting.

This command is designed to be called from within an agent session
(uses GC_AGENT and GC_CITY env vars). It emits an agent.draining event
before blocking.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cmdRuntimeRequestRestart(stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
}

func cmdRuntimeRequestRestart(stdout, stderr io.Writer) int {
	agentName := os.Getenv("GC_AGENT")
	cityDir := os.Getenv("GC_CITY")
	if agentName == "" || cityDir == "" {
		fmt.Fprintln(stderr, "gc runtime request-restart: not in agent context (GC_AGENT/GC_CITY not set)") //nolint:errcheck // best-effort stderr
		return 1
	}

	cfg, err := loadCityConfig(cityDir)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cityName := cfg.Workspace.Name
	if cityName == "" {
		cityName = filepath.Base(cityDir)
	}
	sn := cliSessionName(cityDir, cityName, agentName, cfg.Workspace.SessionTemplate)
	sp := newSessionProvider()
	dops := newDrainOps(sp)
	rec := openCityRecorder(stderr)
	return doRuntimeRequestRestart(dops, rec, agentName, sn, stdout, stderr)
}

// doRuntimeRequestRestart sets the restart-requested flag and blocks forever.
// The controller will kill and restart the session on its next tick.
func doRuntimeRequestRestart(dops drainOps, rec events.Recorder,
	agentName, sn string, stdout, stderr io.Writer,
) int {
	if err := dops.setRestartRequested(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	rec.Record(events.Event{
		Type:    events.SessionDraining,
		Actor:   agentName,
		Subject: agentName,
		Message: "restart requested by agent",
	})
	fmt.Fprintln(stdout, "Restart requested. Blocking until controller kills this session...") //nolint:errcheck // best-effort stdout

	// Block forever. The controller will kill the entire process tree.
	select {}
}

// doRuntimeDrainAck sets the drain-ack flag on the session. The controller
// will stop the session on the next tick.
func doRuntimeDrainAck(dops drainOps, sn string, stdout, stderr io.Writer) int {
	if err := dops.setDrainAck(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	fmt.Fprintln(stdout, "Drain acknowledged. Controller will stop this session.") //nolint:errcheck // best-effort stdout
	return 0
}
