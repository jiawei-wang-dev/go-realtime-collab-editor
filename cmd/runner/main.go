package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/execution"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	limits, socketPath, token, err := loadConfiguration()
	if err != nil {
		return err
	}
	if err := exec.Command("docker", "image", "inspect", limits.Image).Run(); err != nil {
		return fmt.Errorf("fixed sandbox image %q is unavailable; pull it before starting the runner", limits.Image)
	}
	runner, err := execution.NewDockerRunner(limits)
	if err != nil {
		return err
	}
	handler, err := execution.NewHandler(runner, token, limits)
	if err != nil {
		return err
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on runner socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("protect runner socket: %w", err)
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout: limits.Timeout + 5*time.Second, IdleTimeout: 15 * time.Second,
		MaxHeaderBytes: 16 * 1024,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("execution runner ready socket=%s image=%s", socketPath, limits.Image)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runner socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("runner socket path exists and is not a Unix socket: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale runner socket: %w", err)
	}
	return nil
}

func loadConfiguration() (execution.Limits, string, string, error) {
	limits := execution.DefaultLimits()
	limits.Image = envOr("RUNNER_IMAGE", limits.Image)
	var err error
	if limits.Timeout, err = duration("EXECUTION_TIMEOUT", limits.Timeout); err != nil {
		return limits, "", "", err
	}
	if limits.MaxSourceBytes, err = positiveInt64("EXECUTION_MAX_SOURCE_BYTES", limits.MaxSourceBytes); err != nil {
		return limits, "", "", err
	}
	if limits.MaxOutputBytes, err = positiveInt64("EXECUTION_MAX_OUTPUT_BYTES", limits.MaxOutputBytes); err != nil {
		return limits, "", "", err
	}
	if limits.MemoryBytes, err = positiveInt64("EXECUTION_MEMORY_BYTES", limits.MemoryBytes); err != nil {
		return limits, "", "", err
	}
	if limits.PIDs, err = positiveInt64("EXECUTION_PIDS", limits.PIDs); err != nil {
		return limits, "", "", err
	}
	if limits.WorkspaceBytes, err = positiveInt64("EXECUTION_WORKSPACE_BYTES", limits.WorkspaceBytes); err != nil {
		return limits, "", "", err
	}
	if limits.TemporaryBytes, err = positiveInt64("EXECUTION_TEMPORARY_BYTES", limits.TemporaryBytes); err != nil {
		return limits, "", "", err
	}
	if value := os.Getenv("EXECUTION_CPUS"); value != "" {
		limits.CPUs, err = strconv.ParseFloat(value, 64)
		if err != nil || limits.CPUs <= 0 {
			return limits, "", "", errors.New("EXECUTION_CPUS must be positive")
		}
	}
	socketPath := os.Getenv("RUNNER_SOCKET")
	token := os.Getenv("RUNNER_TOKEN")
	if socketPath == "" || len(token) < 32 {
		return limits, "", "", errors.New("RUNNER_SOCKET and a RUNNER_TOKEN of at least 32 bytes are required")
	}
	return limits, socketPath, token, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func positiveInt64(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
}
