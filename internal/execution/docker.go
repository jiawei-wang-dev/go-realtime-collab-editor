package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

type DockerRunner struct {
	limits Limits
}

func NewDockerRunner(limits Limits) (*DockerRunner, error) {
	if limits.Image == "" || limits.Timeout <= 0 || limits.MaxSourceBytes <= 0 || limits.MaxOutputBytes <= 0 || limits.MemoryBytes <= 0 || limits.CPUs <= 0 || limits.PIDs <= 0 || limits.WorkspaceBytes <= 0 || limits.TemporaryBytes <= 0 {
		return nil, errors.New("all Docker runner limits must be positive")
	}
	return &DockerRunner{limits: limits}, nil
}

func (runner *DockerRunner) Run(ctx context.Context, request Request) (Result, error) {
	if request.Language != LanguageGo || len(request.Source) == 0 || int64(len(request.Source)) > runner.limits.MaxSourceBytes {
		return Result{}, ErrInvalidRequest
	}
	name, err := randomContainerName()
	if err != nil {
		return Result{}, err
	}
	// Always attempt removal. CommandContext can terminate the Docker client
	// before Docker has processed --rm, for example when the caller disconnects.
	defer cleanupContainer(name)
	runCtx, cancel := context.WithTimeout(ctx, runner.limits.Timeout)
	defer cancel()
	started := time.Now()
	budget := &outputBudget{remaining: runner.limits.MaxOutputBytes}
	stdout := &limitedBuffer{budget: budget}
	stderr := &limitedBuffer{budget: budget}
	args := runner.dockerArguments(name)
	command := exec.CommandContext(runCtx, "docker", args...)
	command.Stdin = bytes.NewBufferString(request.Source)
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		} else if !timedOut {
			return Result{}, fmt.Errorf("start Docker sandbox: %w", err)
		} else {
			exitCode = -1
		}
	}
	if exitCode == 125 && !timedOut {
		return Result{}, fmt.Errorf("Docker sandbox failed: %s", stderr.String())
	}
	return Result{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		ExitCode:        exitCode,
		DurationMS:      time.Since(started).Milliseconds(),
		TimedOut:        timedOut,
		OutputTruncated: budget.wasTruncated(),
	}, nil
}

func (runner *DockerRunner) dockerArguments(name string) []string {
	return []string{
		"run", "--rm", "-i", "--name", name,
		"--label", "com.collab-editor.sandbox=true",
		"--network", "none",
		"--read-only",
		"--user", "65532:65532",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--memory", strconv.FormatInt(runner.limits.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(runner.limits.MemoryBytes, 10),
		"--cpus", strconv.FormatFloat(runner.limits.CPUs, 'f', -1, 64),
		"--pids-limit", strconv.FormatInt(runner.limits.PIDs, 10),
		"--tmpfs", fmt.Sprintf("/workspace:rw,nosuid,nodev,size=%d,mode=0700,uid=65532,gid=65532", runner.limits.WorkspaceBytes),
		// Go emits the compiled binary under GOTMPDIR, so this isolated and
		// size-limited tmpfs must allow execution. nosuid and nodev remain set.
		"--tmpfs", fmt.Sprintf("/tmp:rw,exec,nosuid,nodev,size=%d,mode=0700,uid=65532,gid=65532", runner.limits.TemporaryBytes),
		"--env", "HOME=/tmp",
		"--env", "GOCACHE=/tmp/go-cache",
		"--env", "GOTMPDIR=/tmp",
		"--env", "GOPROXY=off",
		"--env", "GOSUMDB=off",
		"--env", "GOMAXPROCS=1",
		"--pull", "never",
		"--entrypoint", "/bin/sh",
		runner.limits.Image,
		"-c", "umask 077; cat > /workspace/main.go; exec go run /workspace/main.go",
	}
}

func randomContainerName() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate sandbox name: %w", err)
	}
	return "collab-run-" + hex.EncodeToString(value[:]), nil
}

func cleanupContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
	truncated bool
}

func (budget *outputBudget) take(length int) int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.remaining <= 0 {
		budget.truncated = true
		return 0
	}
	allowed := int64(length)
	if allowed > budget.remaining {
		allowed = budget.remaining
		budget.truncated = true
	}
	budget.remaining -= allowed
	return int(allowed)
}

func (budget *outputBudget) wasTruncated() bool {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.truncated
}

type limitedBuffer struct {
	budget *outputBudget
	buffer bytes.Buffer
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	allowed := buffer.budget.take(len(value))
	if allowed > 0 {
		_, _ = buffer.buffer.Write(value[:allowed])
	}
	return len(value), nil
}

func (buffer *limitedBuffer) String() string { return buffer.buffer.String() }
