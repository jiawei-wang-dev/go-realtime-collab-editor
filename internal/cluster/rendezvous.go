package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

type rankedNode struct {
	node  Node
	score [sha256.Size]byte
}

// RankNodes computes preference only. Actual ownership must be acquired through
// an etcd transaction.
func RankNodes(documentID string, nodes []Node) []Node {
	ranked := make([]rankedNode, 0, len(nodes))
	for _, node := range nodes {
		if !node.IsReady() {
			continue
		}
		ranked = append(ranked, rankedNode{node: node, score: rendezvousScore(documentID, node)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		comparison := bytes.Compare(ranked[i].score[:], ranked[j].score[:])
		if comparison != 0 {
			return comparison > 0
		}
		if ranked[i].node.NodeID != ranked[j].node.NodeID {
			return ranked[i].node.NodeID < ranked[j].node.NodeID
		}
		return ranked[i].node.Incarnation < ranked[j].node.Incarnation
	})

	result := make([]Node, len(ranked))
	for index := range ranked {
		result[index] = ranked[index].node
	}
	return result
}

func rendezvousScore(documentID string, node Node) [sha256.Size]byte {
	hash := sha256.New()
	writeHashPart(hash, documentID)
	writeHashPart(hash, node.NodeID)
	writeHashPart(hash, node.Incarnation)
	var score [sha256.Size]byte
	copy(score[:], hash.Sum(nil))
	return score
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashPart(writer hashWriter, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}
