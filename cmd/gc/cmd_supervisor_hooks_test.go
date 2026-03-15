package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/gastownhall/gascity/internal/supervisor"
)

func init() {
	ensureSupervisorRunningHook = func(stdout, stderr io.Writer) int { return 0 }
	reloadSupervisorHook = func(stdout, stderr io.Writer) int {
		entries, err := supervisor.NewRegistry(supervisor.RegistryPath()).List()
		if err != nil {
			fmt.Fprintf(stderr, "gc supervisor reload (test): %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Path < entries[j].Path
		})
		for _, entry := range entries {
			if code := doStartStandalone([]string{entry.Path}, false, stdout, stderr); code != 0 {
				return code
			}
		}
		return 0
	}
	supervisorAliveHook = func() int { return 0 }
}
