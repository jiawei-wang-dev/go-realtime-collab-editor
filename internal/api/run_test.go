package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/execution"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

type recordingExecutionRunner struct {
	requests []execution.Request
	result   execution.Result
}

func (runner *recordingExecutionRunner) Run(_ context.Context, request execution.Request) (execution.Result, error) {
	runner.requests = append(runner.requests, request)
	return runner.result, nil
}

func TestRunDocumentUsesAuthenticatedWriterAndLiveSource(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "main.go", "package main", "owner"); err != nil {
		t.Fatal(err)
	}
	runner := &recordingExecutionRunner{result: execution.Result{Stdout: "hello\n", ExitCode: 0}}
	application := NewServerWithConfig(Config{Store: store, ExecutionRunner: runner})
	defer application.Shutdown(context.Background())

	body := bytes.NewBufferString(`{"language":"go","source":"package main\nfunc main(){}"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/documents/main.go/run", body)
	request.Header.Set("X-User-ID", "owner")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if len(runner.requests) != 1 || runner.requests[0].Source != "package main\nfunc main(){}" {
		t.Fatalf("runner requests = %#v", runner.requests)
	}
	var result execution.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Stdout != "hello\n" {
		t.Fatalf("result = %#v err=%v", result, err)
	}
}

func TestRunDocumentRejectsReadOnlyUser(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "main.go", "package main", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateOwnership(context.Background(), "main.go", storage.LocalAuthority); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermission(context.Background(), storage.PermissionCommit{
		DocumentID: "main.go", ActorID: "owner", UserID: "reader", Role: permission.RoleRead, Authority: storage.LocalAuthority,
	}); err != nil {
		t.Fatal(err)
	}
	runner := &recordingExecutionRunner{}
	application := NewServerWithConfig(Config{Store: store, ExecutionRunner: runner})
	defer application.Shutdown(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/documents/main.go/run", bytes.NewBufferString(`{"language":"go","source":"package main"}`))
	request.Header.Set("X-User-ID", "reader")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(runner.requests) != 0 {
		t.Fatalf("status=%d runner calls=%d body=%s", response.Code, len(runner.requests), response.Body.String())
	}
}

func TestRunDocumentRejectsInvalidAndOversizedSource(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "main.go", "package main", "owner"); err != nil {
		t.Fatal(err)
	}
	runner := &recordingExecutionRunner{}
	application := NewServerWithConfig(Config{Store: store, ExecutionRunner: runner})
	defer application.Shutdown(context.Background())

	for _, body := range []string{
		`{"language":"python","source":"print(1)"}`,
		`{"language":"go","source":""}`,
		`{"language":"go","source":"` + strings.Repeat("x", 128*1024+1) + `"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/documents/main.go/run", strings.NewReader(body))
		request.Header.Set("X-User-ID", "owner")
		response := httptest.NewRecorder()
		application.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	}
	if len(runner.requests) != 0 {
		t.Fatalf("invalid requests reached runner: %#v", runner.requests)
	}
}

func TestExecutionGateEnforcesConcurrencyAndRate(t *testing.T) {
	gate := newExecutionGate(2, 1, 2, time.Minute)
	now := time.Now()
	release, ok := gate.acquire(now, "user-a")
	if !ok {
		t.Fatal("first execution was rejected")
	}
	if _, ok := gate.acquire(now, "user-a"); ok {
		t.Fatal("concurrent execution for one user was allowed")
	}
	if releaseB, ok := gate.acquire(now, "user-b"); !ok {
		t.Fatal("second global slot was rejected")
	} else {
		releaseB()
	}
	release()
	if release, ok = gate.acquire(now, "user-a"); !ok {
		t.Fatal("second rate-limited execution was rejected")
	}
	release()
	if _, ok := gate.acquire(now, "user-a"); ok {
		t.Fatal("rate limit was not enforced")
	}
}
