package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

type NodeStatus string

const (
	NodeStarting    NodeStatus = "STARTING"
	NodeReady       NodeStatus = "READY"
	NodeDraining    NodeStatus = "DRAINING"
	NodeUnavailable NodeStatus = "UNAVAILABLE"
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Node identifies one service process. Incarnation distinguishes successive
// processes that reuse the same NodeID.
type Node struct {
	NodeID          string     `json:"node_id"`
	Incarnation     string     `json:"incarnation"`
	QUICEndpoint    string     `json:"quic_endpoint"`
	HTTPEndpoint    string     `json:"http_endpoint"`
	BrowserEndpoint string     `json:"browser_endpoint,omitempty"`
	Status          NodeStatus `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
}

func NewIncarnation() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate node incarnation: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (node Node) Validate() error {
	if err := ValidateProcessIdentity(node.NodeID, node.Incarnation); err != nil {
		return err
	}
	if node.QUICEndpoint == "" || node.HTTPEndpoint == "" {
		return errors.New("node QUIC and HTTP endpoints are required")
	}
	if !node.Status.Valid() {
		return fmt.Errorf("invalid node status %q", node.Status)
	}
	if node.StartedAt.IsZero() {
		return errors.New("node start time is required")
	}
	return nil
}

func ValidateProcessIdentity(nodeID, incarnation string) error {
	if !identityPattern.MatchString(nodeID) {
		return errors.New("node ID must contain only letters, digits, '.', '_' or '-' and be at most 128 characters")
	}
	if !identityPattern.MatchString(incarnation) {
		return errors.New("node incarnation must contain only letters, digits, '.', '_' or '-' and be at most 128 characters")
	}
	return nil
}

func (status NodeStatus) Valid() bool {
	switch status {
	case NodeStarting, NodeReady, NodeDraining, NodeUnavailable:
		return true
	default:
		return false
	}
}

func (node Node) Identity() string {
	return node.NodeID + "/" + node.Incarnation
}

func (node Node) IsReady() bool {
	return node.Status == NodeReady
}
