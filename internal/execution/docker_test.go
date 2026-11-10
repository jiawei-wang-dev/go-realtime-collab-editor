package execution

import (
	"strings"
	"testing"
)

func TestDockerArgumentsEnforceSandboxBoundaries(t *testing.T) {
	runner, err := NewDockerRunner(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.dockerArguments("collab-run-test"), " ")
	for _, required := range []string{
		"--network none", "--read-only", "--user 65532:65532", "--cap-drop ALL",
		"--security-opt no-new-privileges:true", "--memory 536870912",
		"--memory-swap 536870912", "--cpus 1", "--pids-limit 64",
		"/workspace:rw,nosuid,nodev,size=16777216", "/tmp:rw,exec,nosuid,nodev,size=268435456",
		"GOPROXY=off", "GOMAXPROCS=1", "--pull never", "--entrypoint /bin/sh", "golang:1.26.10-bookworm",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("Docker arguments do not contain %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "/var/run/docker.sock") || strings.Contains(joined, "--privileged") {
		t.Fatalf("unsafe Docker argument present: %s", joined)
	}
}

func TestLimitedBuffersShareOneOutputBudget(t *testing.T) {
	budget := &outputBudget{remaining: 5}
	stdout := &limitedBuffer{budget: budget}
	stderr := &limitedBuffer{budget: budget}
	_, _ = stdout.Write([]byte("abcd"))
	_, _ = stderr.Write([]byte("WXYZ"))
	if got := stdout.String() + stderr.String(); got != "abcdW" {
		t.Fatalf("combined output = %q", got)
	}
	if !budget.wasTruncated() {
		t.Fatal("expected output truncation")
	}
}
