package cluster

import (
	"testing"
	"time"
)

func TestNodeValidate(t *testing.T) {
	node := Node{NodeID: "node-a", Incarnation: "process-1", QUICEndpoint: "node-a:8443", HTTPEndpoint: "node-a:8080", Status: NodeStarting, StartedAt: time.Now()}
	if err := node.Validate(); err != nil {
		t.Fatalf("validate node: %v", err)
	}
	node.NodeID = "invalid/node"
	if err := node.Validate(); err == nil {
		t.Fatal("expected invalid node ID to be rejected")
	}
}

func TestRankNodesIsDeterministicAndIgnoresUnavailableNodes(t *testing.T) {
	nodes := []Node{
		{NodeID: "node-c", Incarnation: "1", Status: NodeUnavailable},
		{NodeID: "node-a", Incarnation: "1", Status: NodeReady},
		{NodeID: "node-b", Incarnation: "1", Status: NodeReady},
	}
	first := RankNodes("main.go", nodes)
	second := RankNodes("main.go", []Node{nodes[2], nodes[0], nodes[1]})
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected two ready nodes, got %d and %d", len(first), len(second))
	}
	for index := range first {
		if first[index].Identity() != second[index].Identity() {
			t.Fatalf("ranking differs at %d: %q != %q", index, first[index].Identity(), second[index].Identity())
		}
	}
}

func TestRankNodesHasLimitedMovementWhenNodeJoins(t *testing.T) {
	initial := []Node{{NodeID: "a", Incarnation: "1", Status: NodeReady}, {NodeID: "b", Incarnation: "1", Status: NodeReady}}
	expanded := append(append([]Node(nil), initial...), Node{NodeID: "c", Incarnation: "1", Status: NodeReady})
	for index := 0; index < 1000; index++ {
		documentID := string(rune(index))
		before := RankNodes(documentID, initial)[0].Identity()
		after := RankNodes(documentID, expanded)[0].Identity()
		if after != "c/1" && before != after {
			t.Fatalf("document %d moved between existing nodes: %q -> %q", index, before, after)
		}
	}
}

func TestMembershipPublishesIncrementalChanges(t *testing.T) {
	membership := NewMembership()
	updates, cancel := membership.Subscribe(4)
	defer cancel()
	node := Node{NodeID: "node-a", Incarnation: "1", Status: NodeStarting}
	membership.Put(node, 1)
	if event := <-updates; event.Type != NodeJoined {
		t.Fatalf("expected joined event, got %q", event.Type)
	}
	node.Status = NodeReady
	membership.Put(node, 2)
	if event := <-updates; event.Type != NodeUpdated {
		t.Fatalf("expected updated event, got %q", event.Type)
	}
	membership.Delete(node.Identity(), 3)
	if event := <-updates; event.Type != NodeLeft {
		t.Fatalf("expected left event, got %q", event.Type)
	}
}
