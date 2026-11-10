package execution

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixedRunner struct {
	result Result
}

func (runner fixedRunner) Run(context.Context, Request) (Result, error) { return runner.result, nil }

func TestHandlerRequiresTokenAndValidGoSource(t *testing.T) {
	handler, err := NewHandler(fixedRunner{result: Result{Stdout: "ok\n"}}, "01234567890123456789012345678901", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/run", nil)
	unauthorizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedResponse.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/run", strings.NewReader(`{"language":"go","source":"package main"}`))
	request.Header.Set("Authorization", "Bearer 01234567890123456789012345678901")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestUnixClientCallsAuthenticatedRunner(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "collab-runner-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "runner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	token := "01234567890123456789012345678901"
	handler, err := NewHandler(fixedRunner{result: Result{Stdout: "hello\n"}}, token, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	client, err := NewUnixClient(socket, token, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(context.Background(), Request{Language: LanguageGo, Source: "package main"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "hello\n" {
		t.Fatalf("result = %#v", result)
	}
}
