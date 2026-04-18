package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/dispatch"
	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/sourceworkflow"
	"github.com/spf13/cobra"
)

var dispatchControlSessionProvider = newSessionProvider

func sourceWorkflowCommandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// convoyDispatchSubcommands returns the dispatch-related subcommands to add to gc convoy.
func convoyDispatchSubcommands(stdout, stderr io.Writer) []*cobra.Command {
	return []*cobra.Command{
		newConvoyControlCmd(stdout, stderr),
		newConvoyPokeCmd(stdout, stderr),
		newConvoyDeleteCmd(stdout, stderr),
		newConvoyDeleteSourceCmd(stdout, stderr),
		newConvoyReopenSourceCmd(stdout, stderr),
	}
}

// newWorkflowCmd returns a hidden alias for backwards compatibility.
func newWorkflowCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "workflow",
		Short:  "Alias for gc convoy (deprecated)",
		Hidden: true,
	}
	cmd.AddCommand(convoyDispatchSubcommands(stdout, stderr)...)
	return cmd
}

func newConvoyControlCmd(stdout, stderr io.Writer) *cobra.Command {
	var serve bool
	var follow string
	cmd := &cobra.Command{
		Use:   "control [bead-id]",
		Short: "Execute control beads or run the control-dispatcher loop",
		Long: `Process a single control bead, or run the control-dispatcher loop
with --serve to continuously process ready control beads.
Use --follow <agent> to filter the serve loop to a specific agent template.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if serve || follow != "" {
				if follow != "" {
					args = append(args, follow)
				}
				return runConvoyControlServe(args, stdout, stderr)
			}
			if len(args) == 0 {
				return fmt.Errorf("bead-id is required (or use --serve)")
			}
			if err := runControlDispatcher(args[0], stdout, stderr); err != nil {
				if errors.Is(err, dispatch.ErrControlPending) {
					return nil
				}
				fmt.Fprintf(stderr, "gc convoy control: %v\n", err) //nolint:errcheck
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&serve, "serve", false, "Run the control-dispatcher loop (continuous)")
	cmd.Flags().StringVar(&follow, "follow", "", "Run serve loop filtered to a specific agent template")
	return cmd
}

func newConvoyPokeCmd(_ io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "poke",
		Short:  "Trigger immediate control dispatch reconciliation",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc convoy poke: %v\n", err) //nolint:errcheck
				return errExit
			}
			if err := pokeControlDispatch(cityPath); err != nil {
				fmt.Fprintf(stderr, "gc convoy poke: %v\n", err) //nolint:errcheck
				return errExit
			}
			return nil
		},
	}
	return cmd
}

func pokeControlDispatch(cityPath string) error {
	if _, err := sendControllerCommand(cityPath, "control-dispatcher"); err == nil {
		return nil
	}
	return pokeController(cityPath)
}

func runControlDispatcher(beadID string, stdout, _ io.Writer) error {
	cityPath, err := resolveCity()
	if err != nil {
		return err
	}

	// Try all stores (city + rigs) to find the bead.
	store, bead, err := findBeadAcrossStores(cityPath, beadID)
	if err != nil {
		return fmt.Errorf("loading bead %s: %w", beadID, err)
	}

	opts := dispatch.ProcessOptions{CityPath: cityPath}
	opts.Tracef = workflowTracef
	loadCfg := false
	switch bead.Metadata["gc.kind"] {
	case "check", "fanout", "retry-eval", "retry", "ralph":
		loadCfg = true
	}
	if loadCfg {
		cfg, err := loadCityConfig(cityPath)
		if err != nil {
			return err
		}
		resolveRigPaths(cityPath, cfg.Rigs)
		switch bead.Metadata["gc.kind"] {
		case "check", "fanout":
			opts.FormulaSearchPaths = workflowFormulaSearchPaths(cfg, bead)
			opts.PrepareFragment = func(fragment *formula.FragmentRecipe, source beads.Bead) error {
				return decorateDynamicFragmentRecipe(fragment, source, store, cfg.Workspace.Name, cityPath, cfg)
			}
		case "retry-eval":
			sp := dispatchControlSessionProvider()
			opts.RecycleSession = func(subject beads.Bead) error {
				if strings.TrimSpace(subject.Assignee) == "" {
					return fmt.Errorf("subject %s missing assignee for pooled retry recycle", subject.ID)
				}
				return sp.Stop(subject.Assignee)
			}
		case "retry", "ralph":
			opts.FormulaSearchPaths = workflowFormulaSearchPaths(cfg, bead)
			sp := dispatchControlSessionProvider()
			opts.RecycleSession = func(subject beads.Bead) error {
				if strings.TrimSpace(subject.Assignee) == "" {
					return fmt.Errorf("subject %s missing assignee for pooled retry recycle", subject.ID)
				}
				return sp.Stop(subject.Assignee)
			}
		}
	}

	result, err := dispatch.ProcessControl(store, bead, opts)
	if err != nil {
		return err
	}
	if result.Processed {
		fmt.Fprintf(stdout, "control dispatch: bead=%s action=%s", beadID, result.Action) //nolint:errcheck
		if result.Created > 0 {
			fmt.Fprintf(stdout, " created=%d", result.Created) //nolint:errcheck
		}
		if result.Skipped > 0 {
			fmt.Fprintf(stdout, " skipped=%d", result.Skipped) //nolint:errcheck
		}
		fmt.Fprintln(stdout) //nolint:errcheck
	}
	return nil
}

// findBeadAcrossStores preserves the historical city-first lookup semantics.
func findBeadAcrossStores(cityPath, beadID string) (beads.Store, beads.Bead, error) {
	cityStore, err := openStoreAtForCity(cityPath, cityPath)
	if err != nil {
		return nil, beads.Bead{}, fmt.Errorf("opening city store: %w", err)
	}
	if bead, err := cityStore.Get(beadID); err == nil {
		return cityStore, bead, nil
	} else if !errors.Is(err, beads.ErrNotFound) {
		return nil, beads.Bead{}, fmt.Errorf("getting bead %q from %s: %w", beadID, cityPath, err)
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		return nil, beads.Bead{}, err
	}
	for _, dir := range convoyStoreCandidates(cfg, cityPath, beadID) {
		if dir == cityPath {
			continue
		}
		store, err := openStoreAtForCity(dir, cityPath)
		if err != nil {
			return nil, beads.Bead{}, fmt.Errorf("opening store %s: %w", dir, err)
		}
		bead, err := store.Get(beadID)
		if err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				continue
			}
			return nil, beads.Bead{}, fmt.Errorf("getting bead %q from %s: %w", beadID, dir, err)
		}
		return store, bead, nil
	}
	return nil, beads.Bead{}, fmt.Errorf("getting bead %q: %w", beadID, beads.ErrNotFound)
}

func findUniqueBeadAcrossStoresView(cityPath, beadID string) (convoyStoreView, beads.Bead, error) {
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		return convoyStoreView{}, beads.Bead{}, fmt.Errorf("loading city config for bead %q: %w", beadID, err)
	}
	stores, err := openSourceWorkflowStores(cfg, cityPath, beadID)
	if err != nil {
		return convoyStoreView{}, beads.Bead{}, err
	}
	var (
		foundView convoyStoreView
		foundBead beads.Bead
		found     bool
	)
	for _, candidate := range stores {
		bead, err := candidate.store.Get(beadID)
		if err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				continue
			}
			return convoyStoreView{}, beads.Bead{}, fmt.Errorf("getting bead %q from %s: %w", beadID, candidate.path, err)
		}
		if found {
			return convoyStoreView{}, beads.Bead{}, fmt.Errorf(
				"source bead %s exists in multiple stores (%s and %s); source workflow commands require a uniquely resolvable source bead id",
				beadID,
				foundView.path,
				candidate.path,
			)
		}
		foundView = candidate
		foundBead = bead
		found = true
	}
	if !found {
		return convoyStoreView{}, beads.Bead{}, fmt.Errorf("getting bead %q: %w", beadID, beads.ErrNotFound)
	}
	return foundView, foundBead, nil
}

func workflowFormulaSearchPaths(cfg *config.City, bead beads.Bead) []string {
	if cfg == nil {
		return nil
	}
	routedTo := workflowExecutionRoute(bead)
	if routedTo == "" {
		return cfg.FormulaLayers.City
	}
	rigName, _ := config.ParseQualifiedName(routedTo)
	if paths := cfg.FormulaLayers.SearchPaths(rigName); len(paths) > 0 {
		return paths
	}
	return cfg.FormulaLayers.City
}

func decorateDynamicFragmentRecipe(fragment *formula.FragmentRecipe, source beads.Bead, store beads.Store, cityName, cityPath string, cfg *config.City) error {
	if fragment == nil {
		return fmt.Errorf("fragment recipe is nil")
	}
	defaultRoute, err := graphFallbackBindingForBead(source, store, cityName, cfg)
	if err != nil {
		return err
	}
	routingRigContext := graphRouteRigContext(defaultRoute.QualifiedName)
	controlRoute, err := controlDispatcherBinding(store, cityName, cfg, routingRigContext)
	if err != nil {
		return err
	}

	for i := range fragment.Steps {
		step := &fragment.Steps[i]
		if step.Metadata == nil {
			step.Metadata = make(map[string]string)
		} else {
			step.Metadata = maps.Clone(step.Metadata)
		}
		step.Metadata["gc.dynamic_fragment"] = "true"
		propagateDynamicScopeMetadata(step, source)
	}
	formula.ApplyFragmentRecipeGraphControls(fragment)

	stepByID := make(map[string]*formula.RecipeStep, len(fragment.Steps))
	stepAlias := make(map[string]string, len(fragment.Steps))
	for i := range fragment.Steps {
		stepByID[fragment.Steps[i].ID] = &fragment.Steps[i]
		if short, ok := strings.CutPrefix(fragment.Steps[i].ID, fragment.Name+"."); ok {
			stepAlias[short] = fragment.Steps[i].ID
		}
	}
	depsByStep := make(map[string][]string, len(fragment.Deps))
	for _, dep := range fragment.Deps {
		if dep.Type != "blocks" && dep.Type != "waits-for" && dep.Type != "conditional-blocks" {
			continue
		}
		depsByStep[dep.StepID] = append(depsByStep[dep.StepID], dep.DependsOnID)
	}
	bindingCache := make(map[string]graphRouteBinding, len(fragment.Steps))
	resolving := make(map[string]bool, len(fragment.Steps))
	for i := range fragment.Steps {
		step := &fragment.Steps[i]
		switch step.Metadata["gc.kind"] {
		case "workflow", "scope", "ralph", "retry", "spec":
			continue
		}
		binding, err := resolveGraphStepBinding(step.ID, stepByID, stepAlias, depsByStep, bindingCache, resolving, defaultRoute, routingRigContext, store, cityName, cityPath, cfg)
		if err != nil {
			return err
		}
		if isControlDispatcherKind(step.Metadata["gc.kind"]) {
			assignGraphStepRoute(step, binding, &controlRoute)
			continue
		}
		assignGraphStepRoute(step, binding, nil)
	}
	return nil
}

func graphFallbackBindingForBead(source beads.Bead, store beads.Store, cityName string, cfg *config.City) (graphRouteBinding, error) {
	routedTo := workflowExecutionRoute(source)
	if routedTo == "" {
		return graphRouteBinding{SessionName: source.Assignee}, nil
	}
	if cfg == nil {
		return graphRouteBinding{}, fmt.Errorf("graph.v2 routing for %s requires config", source.ID)
	}

	agentCfg, ok := resolveAgentIdentity(cfg, routedTo, graphRouteRigContext(routedTo))
	if !ok {
		return graphRouteBinding{}, fmt.Errorf("unknown graph.v2 fallback target %q on %s", routedTo, source.ID)
	}

	binding := graphRouteBinding{QualifiedName: agentCfg.QualifiedName()}
	if agentCfg.SupportsInstanceExpansion() {
		binding.MetadataOnly = true
		return binding, nil
	}
	if source.Assignee != "" {
		binding.SessionName = source.Assignee
		return binding, nil
	}
	sn := lookupSessionNameOrLegacy(store, cityName, agentCfg.QualifiedName(), cfg.Workspace.SessionTemplate)
	if sn == "" {
		return graphRouteBinding{}, fmt.Errorf("could not resolve session name for %q", agentCfg.QualifiedName())
	}
	binding.SessionName = sn
	return binding, nil
}

func propagateDynamicScopeMetadata(step *formula.RecipeStep, source beads.Bead) {
	if step == nil {
		return
	}
	if step.Metadata == nil {
		step.Metadata = make(map[string]string)
	}
	if scopeRef := strings.TrimSpace(source.Metadata["gc.scope_ref"]); scopeRef != "" && step.Metadata["gc.scope_ref"] == "" {
		step.Metadata["gc.scope_ref"] = scopeRef
	}
	if onFail := strings.TrimSpace(source.Metadata["gc.on_fail"]); onFail != "" && step.Metadata["gc.on_fail"] == "" {
		step.Metadata["gc.on_fail"] = onFail
	}
	for _, key := range []string{"gc.step_id", "gc.ralph_step_id", "gc.attempt"} {
		if value := strings.TrimSpace(source.Metadata[key]); value != "" && step.Metadata[key] == "" {
			step.Metadata[key] = value
		}
	}
	if step.Metadata["gc.scope_ref"] == "" || step.Metadata["gc.scope_role"] != "" {
		return
	}
	switch step.Metadata["gc.kind"] {
	case "scope":
		return
	case "scope-check", "workflow-finalize", "fanout", "check", "retry-eval", "retry", "ralph":
		step.Metadata["gc.scope_role"] = "control"
		return
	default:
		step.Metadata["gc.scope_role"] = "member"
	}
}

func newConvoyDeleteCmd(stdout, stderr io.Writer) *cobra.Command {
	var force bool
	var deleteBeads bool
	cmd := &cobra.Command{
		Use:   "delete <convoy-id>",
		Short: "Close and optionally delete a convoy and all its beads",
		Long: `Close all open beads in a convoy, then optionally delete them.

Searches all stores (city + rigs) for the convoy root and all beads
with matching gc.root_bead_id. Without --force, shows a preview.

By default, beads are closed with gc.outcome=skipped. Use --delete to
also remove them from the store after closing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdWorkflowDelete(args[0], force, deleteBeads, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Actually close/delete (without this, shows preview)")
	cmd.Flags().BoolVar(&deleteBeads, "delete", false, "Also delete beads from the store after closing")
	return cmd
}

func newConvoyDeleteSourceCmd(stdout, stderr io.Writer) *cobra.Command {
	var apply bool
	var deleteBeads bool
	var rigName string
	var storeRef string
	cmd := &cobra.Command{
		Use:   "delete-source <source-bead-id>",
		Short: "Close workflows sourced from a bead",
		Long: `Find every live workflow root sourced from the given bead and close
its subtree. By default this is a preview. Use --apply to mutate.
Use --delete with --apply to also delete closed beads.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if deleteBeads && !apply {
				fmt.Fprintln(stderr, "gc workflow delete-source: --delete requires --apply") //nolint:errcheck
				return errExit
			}
			selector, err := parseSourceWorkflowStoreSelector(rigName, storeRef)
			if err != nil {
				fmt.Fprintf(stderr, "gc workflow delete-source: %v\n", err) //nolint:errcheck
				return errExit
			}
			return exitForCode(cmdWorkflowDeleteSource(args[0], selector, apply, deleteBeads, stdout, stderr))
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually close/delete matched workflows")
	cmd.Flags().BoolVar(&deleteBeads, "delete", false, "Also delete beads from the store after closing")
	cmd.Flags().StringVar(&rigName, "rig", "", "Select the rig store for the source bead")
	cmd.Flags().StringVar(&storeRef, "store-ref", "", "Select the source bead store (city:<name> or rig:<name>)")
	return cmd
}

func newConvoyReopenSourceCmd(stdout, stderr io.Writer) *cobra.Command {
	var rigName string
	var storeRef string
	cmd := &cobra.Command{
		Use:   "reopen-source <source-bead-id>",
		Short: "Reopen a source bead after workflow cleanup",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			selector, err := parseSourceWorkflowStoreSelector(rigName, storeRef)
			if err != nil {
				fmt.Fprintf(stderr, "gc workflow reopen-source: %v\n", err) //nolint:errcheck
				return errExit
			}
			return exitForCode(cmdWorkflowReopenSource(args[0], selector, stdout, stderr))
		},
	}
	cmd.Flags().StringVar(&rigName, "rig", "", "Select the rig store for the source bead")
	cmd.Flags().StringVar(&storeRef, "store-ref", "", "Select the source bead store (city:<name> or rig:<name>)")
	return cmd
}

func cmdWorkflowDelete(workflowID string, force, deleteBeads bool, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow delete: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow delete: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	type storeMatch struct {
		store beads.Store
		beads []beads.Bead
		label string
	}
	var matches []storeMatch

	stores, err := openConvoyStores(cfg, cityPath, workflowID, func(dir string) (beads.Store, error) {
		return openStoreAtForCity(dir, cityPath)
	})
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow delete: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	for _, info := range stores {
		found := findWorkflowBeads(info.store, workflowID)
		if len(found) == 0 {
			continue
		}
		matches = append(matches, storeMatch{
			store: info.store,
			beads: found,
			label: workflowDeleteStoreLabel(cfg, cityPath, info.path),
		})
	}

	total := 0
	openCount := 0
	for _, m := range matches {
		total += len(m.beads)
		for _, b := range m.beads {
			if b.Status != "closed" {
				openCount++
			}
		}
	}
	if total == 0 {
		fmt.Fprintf(stderr, "gc workflow delete: no beads found for workflow %s\n", workflowID) //nolint:errcheck // best-effort stderr
		return 1
	}

	action := "close"
	if deleteBeads {
		action = "delete"
	}
	fmt.Fprintf(stdout, "Workflow %s: %d beads (%d open) — %s\n", workflowID, total, openCount, action) //nolint:errcheck // best-effort stdout
	for _, m := range matches {
		fmt.Fprintf(stdout, "  %s: %d beads\n", m.label, len(m.beads)) //nolint:errcheck // best-effort stdout
	}

	if !force {
		fmt.Fprintln(stdout, "\nDry run. Use --force to proceed.") //nolint:errcheck // best-effort stdout
		return 0
	}

	// Phase 1: Batch close all open beads with gc.outcome=skipped.
	closed := 0
	for _, m := range matches {
		ids := workflowBeadIDs(m.beads)
		n, _ := m.store.CloseAll(ids, map[string]string{"gc.outcome": "skipped"})
		closed += n
	}
	fmt.Fprintf(stdout, "Closed %d open beads\n", closed) //nolint:errcheck // best-effort stdout

	if !deleteBeads {
		return 0
	}

	deleted := 0
	deleteFailed := false
	for _, m := range matches {
		count, errs := deleteWorkflowBeads(m.store, workflowBeadIDs(m.beads))
		deleted += count
		for _, err := range errs {
			deleteFailed = true
			fmt.Fprintf(stderr, "  delete %s: %v\n", m.label, err) //nolint:errcheck // best-effort stderr
		}
	}
	fmt.Fprintf(stdout, "Deleted %d beads\n", deleted) //nolint:errcheck // best-effort stdout
	if deleteFailed {
		return 1
	}
	return 0
}

type sourceWorkflowStoreMatch struct {
	label string
	store beads.Store
	roots []beads.Bead
	beads []beads.Bead
}

type sourceWorkflowStoreSelector struct {
	storeRef string
}

type resolvedSourceWorkflowTarget struct {
	sourceBeadID string
	storeRef     string
	storeView    convoyStoreView
	sourceBead   beads.Bead
}

func parseSourceWorkflowStoreSelector(rigName, storeRef string) (sourceWorkflowStoreSelector, error) {
	rigName = strings.TrimSpace(rigName)
	storeRef = strings.TrimSpace(storeRef)
	if rigName != "" && storeRef != "" {
		return sourceWorkflowStoreSelector{}, fmt.Errorf("--rig and --store-ref are mutually exclusive")
	}
	if rigName != "" {
		storeRef = "rig:" + rigName
	}
	return sourceWorkflowStoreSelector{storeRef: storeRef}, nil
}

func resolveSourceWorkflowTarget(cfg *config.City, cityPath, sourceBeadID string, selector sourceWorkflowStoreSelector, requireSource bool) (resolvedSourceWorkflowTarget, error) {
	sourceBeadID = sourceworkflow.NormalizeSourceBeadID(sourceBeadID)
	target := resolvedSourceWorkflowTarget{sourceBeadID: sourceBeadID}
	if selector.storeRef != "" {
		view, resolvedStoreRef, err := openSourceWorkflowStoreRef(cfg, cityPath, selector.storeRef)
		if err != nil {
			return resolvedSourceWorkflowTarget{}, err
		}
		target.storeRef = resolvedStoreRef
		target.storeView = view
		bead, err := view.store.Get(sourceBeadID)
		switch {
		case err == nil:
			target.sourceBead = bead
		case errors.Is(err, beads.ErrNotFound):
			if requireSource {
				return resolvedSourceWorkflowTarget{}, fmt.Errorf("getting bead %q: %w", sourceBeadID, beads.ErrNotFound)
			}
		default:
			return resolvedSourceWorkflowTarget{}, fmt.Errorf("getting bead %q from %s: %w", sourceBeadID, workflowDeleteStoreLabel(cfg, cityPath, view.path), err)
		}
		return target, nil
	}
	view, bead, err := findUniqueBeadAcrossStoresView(cityPath, sourceBeadID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) && !requireSource {
			return target, nil
		}
		return resolvedSourceWorkflowTarget{}, sourceWorkflowSelectionError(err, sourceBeadID)
	}
	target.storeView = view
	target.sourceBead = bead
	target.storeRef = workflowStoreRefForDir(view.path, cityPath, cfg.Workspace.Name, cfg)
	return target, nil
}

func sourceWorkflowSelectionError(err error, sourceBeadID string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "exists in multiple stores") {
		return fmt.Errorf("%v; rerun with --rig <name> or --store-ref <city:name|rig:name>", err)
	}
	if errors.Is(err, beads.ErrNotFound) {
		return fmt.Errorf("getting bead %q: %w", sourceBeadID, beads.ErrNotFound)
	}
	return err
}

func openSourceWorkflowStoreRef(cfg *config.City, cityPath, storeRef string) (convoyStoreView, string, error) {
	storeRef = strings.TrimSpace(storeRef)
	switch {
	case storeRef == "", storeRef == "city":
		store, err := openStoreAtForCity(cityPath, cityPath)
		if err != nil {
			return convoyStoreView{}, "", fmt.Errorf("opening city store: %w", err)
		}
		cityName := "city"
		if cfg != nil && strings.TrimSpace(cfg.Workspace.Name) != "" {
			cityName = cfg.Workspace.Name
		}
		return convoyStoreView{path: cityPath, store: store}, "city:" + cityName, nil
	case strings.HasPrefix(storeRef, "city:"):
		store, err := openStoreAtForCity(cityPath, cityPath)
		if err != nil {
			return convoyStoreView{}, "", fmt.Errorf("opening city store: %w", err)
		}
		return convoyStoreView{path: cityPath, store: store}, storeRef, nil
	case strings.HasPrefix(storeRef, "rig:"):
		rigName := strings.TrimPrefix(storeRef, "rig:")
		for _, rig := range cfg.Rigs {
			if rig.Name != rigName {
				continue
			}
			rigPath := resolveStoreScopeRoot(cityPath, rig.Path)
			store, err := openStoreAtForCity(rigPath, cityPath)
			if err != nil {
				return convoyStoreView{}, "", fmt.Errorf("opening rig store %s: %w", rigName, err)
			}
			return convoyStoreView{path: rigPath, store: store}, "rig:" + rigName, nil
		}
		return convoyStoreView{}, "", fmt.Errorf("rig %q not found", rigName)
	default:
		return convoyStoreView{}, "", fmt.Errorf("invalid --store-ref %q (want city:<name> or rig:<name>)", storeRef)
	}
}

func applySourceWorkflowMatchCleanup(match sourceWorkflowStoreMatch, deleteBeads bool, stderr io.Writer) (closed, deleted int, incomplete bool) {
	ids := workflowBeadIDs(match.beads)
	n, closeErr := match.store.CloseAll(ids, map[string]string{"gc.outcome": "skipped"})
	closed += n
	if closeErr != nil {
		incomplete = true
		fmt.Fprintf(stderr, "store=%s close_error=%v\n", match.label, closeErr) //nolint:errcheck
		return closed, deleted, incomplete
	}
	if !deleteBeads {
		return closed, deleted, incomplete
	}
	count, errs := deleteWorkflowBeads(match.store, ids)
	deleted += count
	for _, deleteErr := range errs {
		incomplete = true
		fmt.Fprintf(stderr, "store=%s delete_error=%v\n", match.label, deleteErr) //nolint:errcheck
	}
	return closed, deleted, incomplete
}

func cmdWorkflowDeleteSource(sourceBeadID string, selector sourceWorkflowStoreSelector, apply, deleteBeads bool, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow delete-source: %v\n", err) //nolint:errcheck
		return 1
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow delete-source: %v\n", err) //nolint:errcheck
		return 1
	}

	var (
		resultCode int
		runErr     error
	)
	target, err := resolveSourceWorkflowTarget(cfg, cityPath, sourceBeadID, selector, false)
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow delete-source: %v\n", err) //nolint:errcheck
		return 1
	}
	lockScope := target.storeView.path
	if strings.TrimSpace(lockScope) == "" {
		lockScope = cityPath
	}
	ctx, cancel := sourceWorkflowCommandContext()
	defer cancel()
	runErr = sourceworkflow.WithLock(ctx, cityPath, lockScope, sourceBeadID, func() error {
		target, err := resolveSourceWorkflowTarget(cfg, cityPath, sourceBeadID, selector, false)
		if err != nil {
			return err
		}
		matches, err := collectSourceWorkflowMatches(cfg, cityPath, sourceBeadID, target.storeRef)
		if err != nil {
			return err
		}
		if target.storeRef == "" && len(matches) > 1 {
			return fmt.Errorf(
				"source workflow %s has live roots in multiple stores (%s); rerun with --rig <name> or --store-ref <city:name|rig:name>",
				sourceBeadID,
				strings.Join(sourceWorkflowMatchLabels(matches), ", "),
			)
		}
		totalRoots, totalBeads, openCount := summarizeSourceWorkflowMatches(matches)
		if totalRoots == 0 {
			cleared := false
			if apply {
				var clearErr error
				cleared, clearErr = clearSourceWorkflowMetadata(cfg, cityPath, target)
				if clearErr != nil {
					return clearErr
				}
			}
			fmt.Fprintf(
				stdout,
				"result=already_clean source_bead_id=%s matched_roots=0 matched_beads=0 closed=0 deleted=0 metadata_cleared=%t\n",
				sourceBeadID,
				cleared,
			) //nolint:errcheck
			resultCode = 0
			return nil
		}
		if !apply {
			fmt.Fprintf(
				stdout,
				"result=preview source_bead_id=%s matched_roots=%d matched_beads=%d open_beads=%d\n",
				sourceBeadID,
				totalRoots,
				totalBeads,
				openCount,
			) //nolint:errcheck
			for _, match := range matches {
				fmt.Fprintf(stdout, "store=%s roots=%s beads=%d\n", match.label, strings.Join(rootIDs(match.roots), ","), len(match.beads)) //nolint:errcheck
			}
			resultCode = 0
			return nil
		}

		closed := 0
		deleted := 0
		incomplete := false
		for _, match := range matches {
			matchClosed, matchDeleted, matchIncomplete := applySourceWorkflowMatchCleanup(match, deleteBeads, stderr)
			closed += matchClosed
			deleted += matchDeleted
			if matchIncomplete {
				incomplete = true
			}
		}

		stillOpen, verifyErr := countOpenMatchedBeads(matches)
		if verifyErr != nil {
			return verifyErr
		}
		if stillOpen > 0 {
			incomplete = true
		}
		cleared := false
		if !incomplete {
			var clearErr error
			cleared, clearErr = clearSourceWorkflowMetadata(cfg, cityPath, target)
			if clearErr != nil {
				return clearErr
			}
		}
		if incomplete {
			fmt.Fprintf(
				stdout,
				"result=incomplete source_bead_id=%s matched_roots=%d matched_beads=%d closed=%d deleted=%d metadata_cleared=false still_open=%d\n",
				sourceBeadID,
				totalRoots,
				totalBeads,
				closed,
				deleted,
				stillOpen,
			) //nolint:errcheck
			resultCode = 1
			return nil
		}
		fmt.Fprintf(
			stdout,
			"result=cleaned source_bead_id=%s matched_roots=%d matched_beads=%d closed=%d deleted=%d metadata_cleared=%t\n",
			sourceBeadID,
			totalRoots,
			totalBeads,
			closed,
			deleted,
			cleared,
		) //nolint:errcheck
		resultCode = 0
		return nil
	})
	if runErr != nil {
		fmt.Fprintf(stderr, "gc workflow delete-source: %v\n", runErr) //nolint:errcheck
		return 1
	}
	return resultCode
}

func cmdWorkflowReopenSource(sourceBeadID string, selector sourceWorkflowStoreSelector, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow reopen-source: %v\n", err) //nolint:errcheck
		return 1
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow reopen-source: %v\n", err) //nolint:errcheck
		return 1
	}

	resultCode := 0
	target, err := resolveSourceWorkflowTarget(cfg, cityPath, sourceBeadID, selector, true)
	if err != nil {
		fmt.Fprintf(stderr, "gc workflow reopen-source: %v\n", err) //nolint:errcheck
		return 1
	}
	if target.storeView.store == nil || strings.TrimSpace(target.sourceBead.ID) == "" {
		fmt.Fprintf(stderr, "gc workflow reopen-source: getting bead %q: %v\n", sourceBeadID, beads.ErrNotFound) //nolint:errcheck
		return 1
	}
	ctx, cancel := sourceWorkflowCommandContext()
	defer cancel()
	runErr := sourceworkflow.WithLock(ctx, cityPath, target.storeView.path, sourceBeadID, func() error {
		target, err := resolveSourceWorkflowTarget(cfg, cityPath, sourceBeadID, selector, true)
		if err != nil {
			return err
		}
		if target.storeView.store == nil || strings.TrimSpace(target.sourceBead.ID) == "" {
			return fmt.Errorf("getting bead %q: %w", sourceBeadID, beads.ErrNotFound)
		}
		matches, err := collectSourceWorkflowMatches(cfg, cityPath, sourceBeadID, target.storeRef)
		if err != nil {
			return err
		}
		totalRoots, _, _ := summarizeSourceWorkflowMatches(matches)
		if totalRoots > 0 {
			ids := make([]string, 0, totalRoots)
			for _, match := range matches {
				ids = append(ids, rootIDs(match.roots)...)
			}
			fmt.Fprintf(
				stderr,
				"result=conflict source_bead_id=%s blocking_workflow_ids=%s\n",
				sourceBeadID,
				strings.Join(ids, ","),
			) //nolint:errcheck
			resultCode = 3
			return nil
		}
		currentSource, err := target.storeView.store.Get(target.sourceBead.ID)
		if err != nil {
			return err
		}
		open := "open"
		unassigned := ""
		if err := target.storeView.store.SetMetadata(currentSource.ID, "workflow_id", ""); err != nil {
			return err
		}
		if err := target.storeView.store.Update(currentSource.ID, beads.UpdateOpts{
			Status:   &open,
			Assignee: &unassigned,
		}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "result=reopened source_bead_id=%s\n", sourceBeadID) //nolint:errcheck
		return nil
	})
	if runErr != nil {
		fmt.Fprintf(stderr, "gc workflow reopen-source: %v\n", runErr) //nolint:errcheck
		return 1
	}
	return resultCode
}

// findWorkflowBeads returns all beads belonging to a workflow resolved by
// either root bead ID or logical gc.workflow_id, plus descendants keyed by the
// resolved root bead IDs.
func workflowDeleteStoreLabel(cfg *config.City, cityPath, scopePath string) string {
	if scopePath == cityPath {
		return "city"
	}
	if cfg != nil {
		for _, rig := range cfg.Rigs {
			if resolveStoreScopeRoot(cityPath, rig.Path) == scopePath {
				return "rig:" + rig.Name
			}
		}
	}
	return scopePath
}

func deleteWorkflowBeads(store beads.Store, ids []string) (int, []error) {
	deleted := 0
	var errs []error
	for _, id := range ids {
		if err := deleteWorkflowBead(store, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			continue
		}
		deleted++
	}
	return deleted, errs
}

func deleteWorkflowBead(store beads.Store, id string) error {
	downDeps, err := store.DepList(id, "down")
	if err != nil {
		return fmt.Errorf("list down deps: %w", err)
	}
	upDeps, err := store.DepList(id, "up")
	if err != nil {
		return fmt.Errorf("list up deps: %w", err)
	}
	removedDown := make([]beads.Dep, 0, len(downDeps))
	for _, dep := range downDeps {
		if err := store.DepRemove(id, dep.DependsOnID); err != nil {
			return withWorkflowDeleteRestoreError(
				fmt.Errorf("remove down dep %s -> %s: %w", id, dep.DependsOnID, err),
				restoreWorkflowDeleteDeps(store, removedDown, nil),
			)
		}
		removedDown = append(removedDown, dep)
	}
	removedUp := make([]beads.Dep, 0, len(upDeps))
	for _, dep := range upDeps {
		if err := store.DepRemove(dep.IssueID, id); err != nil {
			return withWorkflowDeleteRestoreError(
				fmt.Errorf("remove up dep %s -> %s: %w", dep.IssueID, id, err),
				restoreWorkflowDeleteDeps(store, removedDown, removedUp),
			)
		}
		removedUp = append(removedUp, dep)
	}
	if err := store.Delete(id); err != nil {
		return withWorkflowDeleteRestoreError(
			fmt.Errorf("delete bead: %w", err),
			restoreWorkflowDeleteDeps(store, removedDown, removedUp),
		)
	}
	return nil
}

func withWorkflowDeleteRestoreError(primary, restoreErr error) error {
	if restoreErr == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("rollback failed: %w", restoreErr))
}

func restoreWorkflowDeleteDeps(store beads.Store, downDeps, upDeps []beads.Dep) error {
	var restoreErr error
	for _, dep := range downDeps {
		if err := store.DepAdd(dep.IssueID, dep.DependsOnID, dep.Type); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore dep %s -> %s: %w", dep.IssueID, dep.DependsOnID, err))
		}
	}
	for _, dep := range upDeps {
		if err := store.DepAdd(dep.IssueID, dep.DependsOnID, dep.Type); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore dep %s -> %s: %w", dep.IssueID, dep.DependsOnID, err))
		}
	}
	return restoreErr
}

func collectSourceWorkflowMatches(cfg *config.City, cityPath, sourceBeadID, sourceStoreRef string) ([]sourceWorkflowStoreMatch, error) {
	stores, err := openSourceWorkflowStores(cfg, cityPath, sourceBeadID)
	if err != nil {
		return nil, err
	}
	matches := make([]sourceWorkflowStoreMatch, 0, len(stores))
	for _, info := range stores {
		rootStoreRef := workflowStoreRefForDir(info.path, cityPath, cfg.Workspace.Name, cfg)
		roots, err := sourceworkflow.ListLiveRoots(info.store, sourceBeadID, sourceStoreRef, rootStoreRef)
		if err != nil {
			return nil, err
		}
		if len(roots) == 0 {
			continue
		}
		beadSet := make([]beads.Bead, 0, len(roots))
		for _, root := range roots {
			beadSet = append(beadSet, findWorkflowBeads(info.store, root.ID)...)
		}
		matches = append(matches, sourceWorkflowStoreMatch{
			label: workflowDeleteStoreLabel(cfg, cityPath, info.path),
			store: info.store,
			roots: roots,
			beads: uniqueBeads(beadSet),
		})
	}
	return matches, nil
}

func sourceWorkflowMatchLabels(matches []sourceWorkflowStoreMatch) []string {
	labels := make([]string, 0, len(matches))
	for _, match := range matches {
		labels = append(labels, match.label)
	}
	return labels
}

func summarizeSourceWorkflowMatches(matches []sourceWorkflowStoreMatch) (roots, beadsTotal, openCount int) {
	for _, match := range matches {
		roots += len(match.roots)
		beadsTotal += len(match.beads)
		for _, bead := range match.beads {
			if bead.Status != "closed" {
				openCount++
			}
		}
	}
	return roots, beadsTotal, openCount
}

func countOpenMatchedBeads(matches []sourceWorkflowStoreMatch) (int, error) {
	open := 0
	for _, match := range matches {
		for _, bead := range match.beads {
			current, err := match.store.Get(bead.ID)
			if err != nil {
				if errors.Is(err, beads.ErrNotFound) {
					continue
				}
				return 0, err
			}
			if current.Status != "closed" {
				open++
			}
		}
	}
	return open, nil
}

func openSourceWorkflowStores(cfg *config.City, cityPath, beadID string) ([]convoyStoreView, error) {
	stores := make([]convoyStoreView, 0, len(convoyStoreCandidates(cfg, cityPath, beadID)))
	for _, dir := range convoyStoreCandidates(cfg, cityPath, beadID) {
		store, err := openStoreAtForCity(dir, cityPath)
		if err != nil {
			return nil, fmt.Errorf("opening source workflow store %s: %w", dir, err)
		}
		stores = append(stores, convoyStoreView{path: dir, store: store})
	}
	if len(stores) == 0 {
		return nil, fmt.Errorf("no source workflow stores available")
	}
	return stores, nil
}

func clearSourceWorkflowMetadata(cfg *config.City, cityPath string, target resolvedSourceWorkflowTarget) (bool, error) {
	bead := target.sourceBead
	storeView := target.storeView
	if storeView.store == nil || strings.TrimSpace(storeView.path) == "" {
		if target.storeRef == "" {
			return false, nil
		}
		var err error
		storeView, _, err = openSourceWorkflowStoreRef(cfg, cityPath, target.storeRef)
		if err != nil {
			return false, err
		}
	}
	if strings.TrimSpace(bead.ID) == "" {
		current, err := storeView.store.Get(target.sourceBeadID)
		if err != nil {
			if errors.Is(err, beads.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		bead = current
	}
	if strings.TrimSpace(bead.Metadata["workflow_id"]) == "" {
		return false, nil
	}
	if err := storeView.store.SetMetadata(bead.ID, "workflow_id", ""); err != nil {
		return false, err
	}
	return true, nil
}

func rootIDs(roots []beads.Bead) []string {
	ids := make([]string, 0, len(roots))
	for _, root := range roots {
		if root.ID == "" {
			continue
		}
		ids = append(ids, root.ID)
	}
	return ids
}

func uniqueBeads(bb []beads.Bead) []beads.Bead {
	out := make([]beads.Bead, 0, len(bb))
	seen := make(map[string]struct{}, len(bb))
	for _, bead := range bb {
		if bead.ID == "" {
			continue
		}
		if _, ok := seen[bead.ID]; ok {
			continue
		}
		seen[bead.ID] = struct{}{}
		out = append(out, bead)
	}
	return out
}

func findWorkflowBeads(store beads.Store, workflowID string) []beads.Bead {
	result := make([]beads.Bead, 0, 4)
	seen := make(map[string]struct{}, 4)
	rootIDs := make([]string, 0, 2)
	rootSeen := make(map[string]struct{}, 2)
	addBead := func(b beads.Bead) {
		if b.ID == "" {
			return
		}
		if _, ok := seen[b.ID]; ok {
			return
		}
		seen[b.ID] = struct{}{}
		result = append(result, b)
	}
	addRoot := func(root beads.Bead) {
		resolvedWorkflowID := strings.TrimSpace(root.Metadata["gc.workflow_id"])
		if strings.TrimSpace(root.Metadata["gc.kind"]) != "workflow" {
			return
		}
		if root.ID != workflowID && resolvedWorkflowID != workflowID {
			return
		}
		if _, ok := rootSeen[root.ID]; ok {
			return
		}
		rootSeen[root.ID] = struct{}{}
		rootIDs = append(rootIDs, root.ID)
		addBead(root)
	}
	if root, err := store.Get(workflowID); err == nil {
		addRoot(root)
	}
	if roots, err := store.List(beads.ListQuery{
		Metadata: map[string]string{
			"gc.kind":        "workflow",
			"gc.workflow_id": workflowID,
		},
		IncludeClosed: true,
	}); err == nil {
		for _, root := range roots {
			addRoot(root)
		}
	}
	for _, rootID := range rootIDs {
		all, err := store.List(beads.ListQuery{
			Metadata:      map[string]string{"gc.root_bead_id": rootID},
			IncludeClosed: true,
		})
		if err != nil {
			continue
		}
		for _, b := range all {
			addBead(b)
		}
	}
	return result
}

func workflowBeadIDs(bb []beads.Bead) []string {
	ids := make([]string, len(bb))
	for i, b := range bb {
		ids[i] = b.ID
	}
	return ids
}
