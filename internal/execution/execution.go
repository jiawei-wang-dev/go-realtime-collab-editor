package execution

import (
	"context"
	"errors"
	"time"
)

const LanguageGo = "go"

var (
	ErrInvalidRequest    = errors.New("invalid execution request")
	ErrRunnerUnavailable = errors.New("execution runner unavailable")
)

type Limits struct {
	Image          string
	Timeout        time.Duration
	MaxSourceBytes int64
	MaxOutputBytes int64
	MemoryBytes    int64
	CPUs           float64
	PIDs           int64
	WorkspaceBytes int64
	TemporaryBytes int64
}

func DefaultLimits() Limits {
	return Limits{
		Image:          "golang:1.26.10-bookworm",
		Timeout:        10 * time.Second,
		MaxSourceBytes: 128 * 1024,
		MaxOutputBytes: 64 * 1024,
		MemoryBytes:    512 * 1024 * 1024,
		CPUs:           1,
		PIDs:           64,
		WorkspaceBytes: 16 * 1024 * 1024,
		TemporaryBytes: 256 * 1024 * 1024,
	}
}

type Request struct {
	Language string `json:"language"`
	Source   string `json:"source"`
}

type Result struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	DurationMS      int64  `json:"duration_ms"`
	TimedOut        bool   `json:"timed_out"`
	OutputTruncated bool   `json:"output_truncated"`
}

type Runner interface {
	Run(ctx context.Context, request Request) (Result, error)
}
