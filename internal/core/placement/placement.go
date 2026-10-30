// Package placement evaluates Docker Swarm placement constraints against nodes.
// It is pure (stdlib + core/model only) and shared by the healer — which needs a
// recovered, constraint-satisfying node — and the rebalancer — which must only
// propose moving a task to a node the service is actually allowed to run on.
//
// Two evaluation modes exist because "conservative" points in opposite
// directions for the two callers. NodeSatisfies (the rebalancer) skips a
// constraint it cannot parse or whose key it does not recognise, so exotic
// constraint forms never silently widen exclusion. NodeSatisfiesStrict (the
// healer) treats such a constraint as unmet: there a false "satisfied" is what
// triggers a force-update of a healthy service. In both modes a parseable,
// recognised constraint the node violates makes the whole node unsatisfying.
package placement

import (
	"strings"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// Constraint operators understood by the evaluator. Anything else is treated as
// unparseable and conservatively ignored.
const (
	opEq  = "=="
	opNeq = "!="
)

// Constraint key prefixes/keys the evaluator can resolve. Keys outside this set
// (e.g. engine.labels.*, node.platform.*) are unknown: skipped by NodeSatisfies,
// unmet for NodeSatisfiesStrict.
const (
	keyHostname    = "node.hostname"
	keyID          = "node.id"
	keyRole        = "node.role"
	labelKeyPrefix = "node.labels."
)

// NodeSatisfies reports whether the node meets every placement constraint that
// the evaluator can assess. Constraints it cannot parse, or whose key it does
// not recognise, are skipped rather than treated as failures. An empty
// constraint set is vacuously satisfied.
func NodeSatisfies(n model.NodeView, constraints []string) bool {
	return satisfies(n, constraints, false)
}

// NodeSatisfiesStrict is NodeSatisfies for callers whose false positive is the
// dangerous outcome: a constraint it cannot parse, or whose key it does not
// recognise, makes the node unsatisfying instead of being skipped.
func NodeSatisfiesStrict(n model.NodeView, constraints []string) bool {
	return satisfies(n, constraints, true)
}

func satisfies(n model.NodeView, constraints []string, strict bool) bool {
	for _, c := range constraints {
		key, op, value, ok := parseConstraint(c)
		if !ok {
			if strict {
				return false
			}
			continue // unparseable: never exclude
		}
		actual, known := nodeValue(n, key)
		if !known {
			if strict {
				return false
			}
			continue // key we cannot evaluate: never exclude
		}
		equal := actual == value
		switch op {
		case opEq:
			if !equal {
				return false
			}
		case opNeq:
			if equal {
				return false
			}
		}
	}
	return true
}

// nodeValue resolves the node attribute addressed by a constraint key, and
// whether the evaluator knows how to assess that key. A missing node.labels.*
// key yields ("", true): the node is known not to carry the label, so an `==`
// constraint fails and a `!=` constraint holds — matching Swarm's semantics.
func nodeValue(n model.NodeView, key string) (value string, known bool) {
	switch {
	case key == keyHostname:
		return n.Name, true
	case key == keyID:
		return n.ID, true
	case key == keyRole:
		return n.Role, true
	case strings.HasPrefix(key, labelKeyPrefix):
		return n.Labels[strings.TrimPrefix(key, labelKeyPrefix)], true
	default:
		return "", false
	}
}

// parseConstraint splits a Docker placement constraint of the simple equality
// form "<key> <op> <value>" (e.g. "node.labels.nodeNum == 1") into its parts.
// Surrounding whitespace is trimmed. ok is false when no ==/!= operator is found
// or either side is empty, in which case the caller ignores the constraint.
func parseConstraint(raw string) (key, op, value string, ok bool) {
	for _, o := range []string{opEq, opNeq} {
		if i := strings.Index(raw, o); i >= 0 {
			key = strings.TrimSpace(raw[:i])
			value = strings.TrimSpace(raw[i+len(o):])
			if key == "" || value == "" {
				return "", "", "", false
			}
			return key, o, value, true
		}
	}
	return "", "", "", false
}
