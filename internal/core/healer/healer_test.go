package healer

import (
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// healer test fixtures.
var (
	// t0 is the moment the pending task entered pending.
	t0        = time.Unix(1_700_000_000, 0)
	threshold = 2 * time.Minute
	nowBeyond = t0.Add(3 * time.Minute) // pending duration above threshold
	nowBelow  = t0.Add(1 * time.Minute) // pending duration below threshold
)

func svcWith(constraints ...string) model.ManagedService {
	return model.ManagedService{
		Ref:         model.ServiceRef{ID: "s1", Name: "web"},
		Replicated:  true,
		Constraints: constraints,
		Policy:      model.ServicePolicy{Enabled: true, Min: 1, Max: 5, Metric: "cpu", Target: 80},
	}
}

func activeNode() model.NodeView {
	return model.NodeView{
		ID: "n1", Name: "host-1",
		Availability: model.NodeAvailabilityActive,
		State:        model.NodeStateReady,
		Labels:       map[string]string{"nodeNum": "1"},
	}
}

func downNode() model.NodeView {
	n := activeNode()
	n.State = "down" // node not yet recovered
	return n
}

// Real Swarm scheduler errors for a pending task.
const (
	errConstraints = "no suitable node (scheduling constraints not satisfied on 1 node)"
	errResources   = "no suitable node (insufficient resources on 1 node)"
	errMixed       = "no suitable node (scheduling constraints not satisfied on 2 nodes; insufficient resources on 1 node)"
)

// pendingSince is a task pending on placement constraints — the healable case.
func pendingSince(id string, since time.Time) model.TaskView {
	return pendingWithErr(id, since, errConstraints)
}

func pendingWithErr(id string, since time.Time, err string) model.TaskView {
	return model.TaskView{
		ID: id, ServiceID: "s1",
		State: model.TaskStatePending, DesiredState: model.TaskStateRunning,
		Err:   err,
		Since: since,
	}
}

func managerNode() model.NodeView {
	n := activeNode()
	n.Role = "manager"
	return n
}

func TestDetectStuckSignature(t *testing.T) {
	v := Detect(
		svcWith("node.labels.nodeNum==1"),
		[]model.TaskView{pendingSince("t9", t0)},
		[]model.NodeView{activeNode()},
		threshold, nowBeyond,
	)
	if !v.Stuck {
		t.Fatalf("expected stuck verdict, got %+v", v)
	}
	if v.TaskID != "t9" {
		t.Errorf("TaskID = %q, want t9", v.TaskID)
	}
	if v.NodeID != "n1" {
		t.Errorf("NodeID = %q, want n1", v.NodeID)
	}
}

func TestDetectStuckOnRoleConstraint(t *testing.T) {
	v := Detect(
		svcWith("node.role==manager"),
		[]model.TaskView{pendingSince("t9", t0)},
		[]model.NodeView{managerNode()},
		threshold, nowBeyond,
	)
	if !v.Stuck {
		t.Fatalf("expected stuck verdict on a recovered manager, got %+v", v)
	}
}

func TestDetectNotStuck(t *testing.T) {
	cases := []struct {
		name  string
		svc   model.ManagedService
		tasks []model.TaskView
		nodes []model.NodeView
		now   time.Time
	}{
		{
			name:  "node still down",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{downNode()},
			now:   nowBeyond,
		},
		{
			name:  "pending below threshold",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBelow,
		},
		{
			name:  "no placement constraints",
			svc:   svcWith(),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name: "task running not pending",
			svc:  svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{{
				ID: "t1", State: model.TaskStateRunning, DesiredState: model.TaskStateRunning,
			}},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "active node does not satisfy constraint",
			svc:   svcWith("node.labels.nodeNum==2"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "pending on resources, not constraints",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingWithErr("t1", t0, errResources)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "pending on constraints and resources",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingWithErr("t1", t0, errMixed)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "pending with no scheduler error yet",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingWithErr("t1", t0, "")},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "role constraint on a worker",
			svc:   svcWith("node.role==manager"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{func() model.NodeView { n := activeNode(); n.Role = "worker"; return n }()},
			now:   nowBeyond,
		},
		{
			name:  "unknown constraint key never makes a node recovered",
			svc:   svcWith("engine.labels.foo==bar"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: []model.NodeView{activeNode()},
			now:   nowBeyond,
		},
		{
			name:  "no nodes at all",
			svc:   svcWith("node.labels.nodeNum==1"),
			tasks: []model.TaskView{pendingSince("t1", t0)},
			nodes: nil,
			now:   nowBeyond,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Detect(tc.svc, tc.tasks, tc.nodes, threshold, tc.now)
			if v.Stuck {
				t.Errorf("expected not-stuck, got stuck verdict: %+v", v)
			}
			if v.Reason == "" {
				t.Error("a not-stuck verdict must carry a reason")
			}
		})
	}
}
