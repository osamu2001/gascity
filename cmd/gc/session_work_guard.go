package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func buildAssignedWorkIndex(workBeads []beads.Bead) map[string]bool {
	if workBeads == nil {
		return nil
	}
	index := make(map[string]bool, len(workBeads))
	for _, wb := range workBeads {
		if wb.Status != "open" && wb.Status != "in_progress" {
			continue
		}
		assignee := strings.TrimSpace(wb.Assignee)
		if assignee == "" {
			continue
		}
		index[assignee] = true
	}
	return index
}

// closeSessionBeadIfUnassigned closes a session bead only when the live
// store confirms no open or in-progress work is assigned to it. Callers
// must NOT pass a pre-computed work snapshot — this helper queries the
// store itself so its decision cannot be poisoned by a stale snapshot
// taken earlier in the tick (see the PR that retired the snapshot-based
// variant). Live-query failures fail closed: the bead stays open until
// assignment can be re-verified.
func closeSessionBeadIfUnassigned(
	store beads.Store,
	session beads.Bead,
	reason string,
	now time.Time,
	stderr io.Writer,
) bool {
	if stderr == nil {
		stderr = io.Discard
	}
	hasAssignedWork, err := sessionHasOpenAssignedWork(store, session)
	if err != nil {
		fmt.Fprintf(stderr, "session work guard: checking assigned work for %s: %v\n", session.ID, err) //nolint:errcheck
		return false
	}
	if hasAssignedWork {
		return false
	}
	return closeBead(store, session.ID, reason, now, stderr)
}
