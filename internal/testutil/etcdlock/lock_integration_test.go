package etcdlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestEtcdLockSerializationTimeoutAndCleanup(t *testing.T) {
	firstClient := integrationClient(t)
	defer firstClient.Close()
	secondClient := integrationClient(t)
	defer secondClient.Close()
	namespace := fmt.Sprintf("/collab-test-lock-tests/serialization-%d", time.Now().UnixNano())

	firstRelease, err := Acquire(context.Background(), firstClient, namespace, 3)
	if err != nil {
		t.Fatal(err)
	}
	secondStarted := make(chan struct{})
	secondAcquired := make(chan func() error, 1)
	secondErrors := make(chan error, 1)
	go func() {
		close(secondStarted)
		release, acquireErr := Acquire(context.Background(), secondClient, namespace, 3)
		if acquireErr != nil {
			secondErrors <- acquireErr
			return
		}
		secondAcquired <- release
	}()
	<-secondStarted
	select {
	case release := <-secondAcquired:
		_ = release()
		t.Fatal("second client acquired the held lock")
	case err := <-secondErrors:
		t.Fatalf("second lock failed before release: %v", err)
	default:
	}
	if err := firstRelease(); err != nil {
		t.Fatal(err)
	}
	select {
	case release := <-secondAcquired:
		if err := release(); err != nil {
			t.Fatal(err)
		}
	case err := <-secondErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("second client did not acquire after release")
	}

	heldRelease, err := Acquire(context.Background(), firstClient, namespace, 3)
	if err != nil {
		t.Fatal(err)
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if release, err := Acquire(timeoutCtx, secondClient, namespace, 3); err == nil {
		_ = release()
		t.Fatal("lock acquisition ignored context timeout")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if err := heldRelease(); err != nil {
		t.Fatal(err)
	}

	fixtureKey := fmt.Sprintf("/collab-test-fixtures/preserved-%d", time.Now().UnixNano())
	if _, err := firstClient.Put(context.Background(), fixtureKey, "preserve"); err != nil {
		t.Fatal(err)
	}
	defer firstClient.Delete(context.Background(), fixtureKey)
	release, err := Acquire(context.Background(), firstClient, namespace, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	response, err := firstClient.Get(context.Background(), fixtureKey)
	if err != nil || len(response.Kvs) != 1 || string(response.Kvs[0].Value) != "preserve" {
		t.Fatalf("unrelated fixture was changed: %#v, %v", response.Kvs, err)
	}
}

func TestEtcdLockLeaseExpiresAfterClientLoss(t *testing.T) {
	crashedClient := integrationClient(t)
	namespace := fmt.Sprintf("/collab-test-lock-tests/lease-%d", time.Now().UnixNano())
	if _, err := Acquire(context.Background(), crashedClient, namespace, 2); err != nil {
		t.Fatal(err)
	}
	if err := crashedClient.Close(); err != nil {
		t.Fatal(err)
	}

	replacement := integrationClient(t)
	defer replacement.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	release, err := Acquire(ctx, replacement, namespace, 2)
	if err != nil {
		t.Fatalf("lease-backed lock was not recovered: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestEtcdLockUnavailableFailsClosed(t *testing.T) {
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{"127.0.0.1:1"}, DialTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if release, err := Acquire(ctx, client, "/collab-test-lock-tests/unavailable", 2); err == nil {
		_ = release()
		t.Fatal("unavailable etcd allowed lock acquisition")
	}
}

func integrationClient(t *testing.T) *clientv3.Client {
	t.Helper()
	endpoints := os.Getenv("ETCD_TEST_ENDPOINTS")
	if endpoints == "" {
		t.Skip("ETCD_TEST_ENDPOINTS is not configured")
	}
	client, err := Open(Config{
		Endpoints: strings.Split(endpoints, ","), CAFile: os.Getenv("ETCD_TEST_CA_FILE"),
		CertificateFile: os.Getenv("ETCD_TEST_CERT_FILE"), KeyFile: os.Getenv("ETCD_TEST_KEY_FILE"), DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
