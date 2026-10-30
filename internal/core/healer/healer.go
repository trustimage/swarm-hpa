package healer

import (
	"fmt"
	"strings"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/placement"
)

// Verdict is the result of stuck-task detection for a single service. It is a
// plain value so callers can log the reason and act on Stuck without re-deriving
// anything.
type Verdict struct {
	// Stuck reports whether the service matches the full stuck-pending
	// signature and therefore warrants a force-update.
	Stuck bool
	// Reason is a short, human-readable explanation of the verdict (stuck or
	// not), suitable for structured logs.
	Reason string
	// TaskID is the long-pending task that triggered a stuck verdict (empty
	// when not stuck).
	TaskID string
	// NodeID is the recovered, constraint-satisfying node that makes the task
	// healable (empty when not stuck).
	NodeID string
}

// Detect decides whether a service is genuinely stuck and force-updatable,
// applying the precise 5-point signature from moby/moby#42215. It is pure: a
// deterministic function of its inputs (no I/O, no clock).
//
// A service is Stuck only when ALL of the following hold:
//  1. it carries placement constraints,
//  2. at least one of its tasks is pending while Swarm wants it running, and has
//     been so for at least `threshold` (now - task.Since >= threshold),
//  3. that task's scheduler error says it is pending on placement constraints
//     and NOT on resources (see constraintBlocked), and
//  4. some cluster node both satisfies every constraint (strictly) AND is now
//     Active+Ready — i.e. the constrained target has recovered.
//
// The signature is deliberately conservative: a false positive force-restarts a
// healthy production service. A task pending for lack of CPU/memory is not
// stuck — a force-update would only restart the running replicas and leave it
// pending — so (3) excludes it. Constraints are evaluated with
// placement.NodeSatisfiesStrict: an unparseable or unknown-key constraint makes
// a node unsatisfying, so it can never manufacture a "recovered" node.
func Detect(svc model.ManagedService, tasks []model.TaskView, nodes []model.NodeView, threshold time.Duration, now time.Time) Verdict {
	if len(svc.Constraints) == 0 {
		return Verdict{Reason: "no placement constraints"}
	}

	pending, ok := longestPending(tasks, threshold, now)
	if !ok {
		return Verdict{Reason: fmt.Sprintf("no task pending on placement constraints beyond %s", threshold)}
	}

	for _, n := range nodes {
		if n.IsActive() && placement.NodeSatisfiesStrict(n, svc.Constraints) {
			return Verdict{
				Stuck:  true,
				Reason: fmt.Sprintf("task pending >= %s and constraint-satisfying node is Active+Ready", threshold),
				TaskID: pending.ID,
				NodeID: n.ID,
			}
		}
	}

	return Verdict{Reason: "no constraint-satisfying node is Active+Ready"}
}

// longestPending returns the first task that is pending+desired-running, has
// been pending for at least `threshold`, and is blocked on placement constraints
// rather than resources, and whether such a task exists.
func longestPending(tasks []model.TaskView, threshold time.Duration, now time.Time) (model.TaskView, bool) {
	for _, t := range tasks {
		if t.IsPending() && now.Sub(t.Since) >= threshold && constraintBlocked(t.Err) {
			return t, true
		}
	}
	return model.TaskView{}, false
}

// Swarm scheduler error fragments, e.g. "no suitable node (scheduling
// constraints not satisfied on 3 nodes; insufficient resources on 1 node)".
const (
	errConstraintsNotSatisfied = "scheduling constraints not satisfied"
	errInsufficientResources   = "insufficient resources"
)

// constraintBlocked reports whether a pending task's scheduler error blames
// placement constraints and nothing about resources. An empty error (no reason
// known yet) or any mention of insufficient resources is not healable: a
// force-update cannot create capacity.
func constraintBlocked(err string) bool {
	return strings.Contains(err, errConstraintsNotSatisfied) &&
		!strings.Contains(err, errInsufficientResources)
}
