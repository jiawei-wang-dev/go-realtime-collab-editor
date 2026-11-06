package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/testutil/etcdlock"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestRegistryIntegration(t *testing.T) {
	acquireIntegrationLock(t)
	client := openIntegrationClient(t)
	defer client.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	nodeA := integrationNode("phase82-a-"+suffix, "process-a")
	nodeB := integrationNode("phase82-b-"+suffix, "process-b")
	registryA := startIntegrationRegistry(t, client, nodeA, 3*time.Second)
	defer closeRegistry(t, registryA)
	registryB := startIntegrationRegistry(t, client, nodeB, 3*time.Second)

	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatalf("mark node A ready: %v", err)
	}
	if err := registryB.MarkReady(context.Background()); err != nil {
		t.Fatalf("mark node B ready: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return membershipContains(registryA.Membership(), nodeA.Identity(), cluster.NodeReady) &&
			membershipContains(registryA.Membership(), nodeB.Identity(), cluster.NodeReady) &&
			membershipContains(registryB.Membership(), nodeA.Identity(), cluster.NodeReady)
	})

	closeRegistry(t, registryB)
	waitFor(t, 5*time.Second, func() bool {
		return !membershipContains(registryA.Membership(), nodeB.Identity(), cluster.NodeReady)
	})
}

func TestLeaseExpiryRemovesNode(t *testing.T) {
	acquireIntegrationLock(t)
	client := openIntegrationClient(t)
	defer client.Close()
	observer := startIntegrationRegistry(t, client, integrationNode(fmt.Sprintf("phase82-observer-%d", time.Now().UnixNano()), "observer"), 3*time.Second)
	defer closeRegistry(t, observer)

	expiring := integrationNode(fmt.Sprintf("phase82-expiring-%d", time.Now().UnixNano()), "expiring")
	payload, err := encodeNode(expiring)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := client.client.Grant(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.client.Put(context.Background(), nodeKey(expiring), string(payload), clientv3.WithLease(lease.ID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return membershipContains(observer.Membership(), expiring.Identity(), cluster.NodeStarting)
	})
	waitFor(t, 8*time.Second, func() bool {
		return !membershipContains(observer.Membership(), expiring.Identity(), cluster.NodeStarting)
	})
}

func TestWatchRecoversAfterCompaction(t *testing.T) {
	acquireIntegrationLock(t)
	client := openIntegrationClient(t)
	defer client.Close()
	node := integrationNode(fmt.Sprintf("phase82-compact-%d", time.Now().UnixNano()), "process")
	payload, err := encodeNode(node)
	if err != nil {
		t.Fatal(err)
	}
	putResponse, err := client.client.Put(context.Background(), nodeKey(node), string(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer client.client.Delete(context.Background(), nodeKey(node))
	if _, err := client.client.Compact(context.Background(), putResponse.Header.Revision); err != nil {
		t.Fatal(err)
	}

	registry := &Registry{client: client, membership: cluster.NewMembership()}
	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go registry.watchMembership(watchCtx, putResponse.Header.Revision)
	waitFor(t, 5*time.Second, func() bool {
		return membershipContains(registry.membership, node.Identity(), cluster.NodeStarting)
	})
}

func TestUnavailableEtcdFailsClosed(t *testing.T) {
	config := integrationConfig(t)
	config.Endpoints = []string{"https://127.0.0.1:1"}
	config.DialTimeout = 300 * time.Millisecond
	config.RequestTimeout = 500 * time.Millisecond
	if client, err := Open(context.Background(), config); err == nil {
		client.Close()
		t.Fatal("unavailable etcd endpoint was accepted")
	}
}

func TestEtcdTLSRejectsUnknownCA(t *testing.T) {
	wrongCA := os.Getenv("ETCD_TEST_WRONG_CA_FILE")
	if wrongCA == "" {
		t.Skip("ETCD_TEST_WRONG_CA_FILE is not configured")
	}
	config := integrationConfig(t)
	config.CAFile = wrongCA
	config.DialTimeout = time.Second
	config.RequestTimeout = 2 * time.Second
	if client, err := Open(context.Background(), config); err == nil {
		client.Close()
		t.Fatal("etcd server signed by an unknown CA was accepted")
	}
}

func openIntegrationClient(t *testing.T) *Client {
	t.Helper()
	client, err := Open(context.Background(), integrationConfig(t))
	if err != nil {
		t.Fatalf("open integration etcd: %v", err)
	}
	return client
}

func integrationConfig(t *testing.T) Config {
	t.Helper()
	endpoints := os.Getenv("ETCD_TEST_ENDPOINTS")
	if endpoints == "" {
		t.Skip("ETCD_TEST_ENDPOINTS is not configured")
	}
	return Config{
		Endpoints:       strings.Split(endpoints, ","),
		CAFile:          os.Getenv("ETCD_TEST_CA_FILE"),
		CertificateFile: os.Getenv("ETCD_TEST_CERT_FILE"),
		KeyFile:         os.Getenv("ETCD_TEST_KEY_FILE"),
		DialTimeout:     3 * time.Second,
		RequestTimeout:  3 * time.Second,
	}
}

func integrationNode(nodeID, incarnation string) cluster.Node {
	return cluster.Node{
		NodeID:       nodeID,
		Incarnation:  incarnation,
		QUICEndpoint: "127.0.0.1:8443",
		HTTPEndpoint: "127.0.0.1:8080",
		Status:       cluster.NodeStarting,
		StartedAt:    time.Now().UTC(),
	}
}

func startIntegrationRegistry(t *testing.T, client *Client, node cluster.Node, ttl time.Duration) *Registry {
	t.Helper()
	registry, err := NewRegistry(client, node, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return registry
}

func closeRegistry(t *testing.T, registry *Registry) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := registry.Close(ctx); err != nil {
		t.Errorf("close registry: %v", err)
	}
}

func membershipContains(membership *cluster.Membership, identity string, status cluster.NodeStatus) bool {
	nodes, _ := membership.Snapshot()
	for _, node := range nodes {
		if node.Identity() == identity && node.Status == status {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

func encodeNode(node cluster.Node) ([]byte, error) {
	return json.Marshal(node)
}

func acquireIntegrationLock(t *testing.T) {
	t.Helper()
	endpoints := os.Getenv("ETCD_TEST_ENDPOINTS")
	if endpoints == "" {
		t.Skip("ETCD_TEST_ENDPOINTS is not configured")
	}
	client, err := etcdlock.Open(etcdlock.Config{
		Endpoints: strings.Split(endpoints, ","), CAFile: os.Getenv("ETCD_TEST_CA_FILE"),
		CertificateFile: os.Getenv("ETCD_TEST_CERT_FILE"), KeyFile: os.Getenv("ETCD_TEST_KEY_FILE"), DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	release, err := etcdlock.Acquire(ctx, client, etcdlock.SharedMembership, 5)
	if err != nil {
		cancel()
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release shared etcd test lock: %v", err)
		}
		cancel()
		_ = client.Close()
	})
}
