package execution

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDockerRunnerRealSandbox(t *testing.T) {
	if os.Getenv("RUN_DOCKER_INTEGRATION") != "true" {
		t.Skip("set RUN_DOCKER_INTEGRATION=true to run the real Docker sandbox tests")
	}
	if err := exec.Command("docker", "image", "inspect", DefaultLimits().Image).Run(); err != nil {
		t.Fatalf("fixed runner image is unavailable: %v", err)
	}

	t.Run("normal program", func(t *testing.T) {
		limits := DefaultLimits()
		limits.Timeout = 20 * time.Second // Includes a first-use compile from a disposable cache.
		result := runReal(t, limits, `package main
import "fmt"
func main() { fmt.Println("Hello, World!") }`)
		if result.ExitCode != 0 || result.Stdout != "Hello, World!\n" || result.TimedOut {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("loop output", func(t *testing.T) {
		result := runReal(t, DefaultLimits(), `package main
import "fmt"
func main() { for i := 1; i <= 5; i++ { fmt.Println(i) } }`)
		if result.ExitCode != 0 || result.Stdout != "1\n2\n3\n4\n5\n" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("compile error", func(t *testing.T) {
		result := runReal(t, DefaultLimits(), `package main
func main() { fmt.Println("hello") }`)
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "undefined: fmt") {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("runtime panic", func(t *testing.T) {
		result := runReal(t, DefaultLimits(), `package main
func main() { panic("test panic") }`)
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "panic: test panic") {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("runtime limits visible to Docker", func(t *testing.T) {
		limits := DefaultLimits()
		limits.Timeout = 3 * time.Second
		runner, err := NewDockerRunner(limits)
		if err != nil {
			t.Fatal(err)
		}
		resultChannel := make(chan Result, 1)
		errorChannel := make(chan error, 1)
		go func() {
			result, runErr := runner.Run(context.Background(), Request{Language: LanguageGo, Source: "package main\nfunc main(){for{}}"})
			resultChannel <- result
			errorChannel <- runErr
		}()

		var containerName string
		deadline := time.NewTimer(2 * time.Second)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer deadline.Stop()
		defer ticker.Stop()
		for containerName == "" {
			select {
			case <-deadline.C:
				t.Fatal("sandbox container did not become visible")
			case <-ticker.C:
				output, listErr := exec.Command("docker", "ps", "--filter", "label=com.collab-editor.sandbox=true", "--format", "{{.Names}}").Output()
				if listErr != nil {
					t.Fatal(listErr)
				}
				containerName = strings.TrimSpace(string(output))
			}
		}
		format := "{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.NetworkMode}} {{json .HostConfig.CapDrop}} {{json .HostConfig.SecurityOpt}} {{.Config.User}}"
		inspected, err := exec.Command("docker", "inspect", "--format", format, containerName).Output()
		if err != nil {
			t.Fatal(err)
		}
		configuration := string(inspected)
		for _, required := range []string{"536870912", "1000000000", "64", "true", "none", "ALL", "no-new-privileges:true", "65532:65532"} {
			if !strings.Contains(configuration, required) {
				t.Fatalf("runtime configuration does not contain %q: %s", required, configuration)
			}
		}
		if err := <-errorChannel; err != nil {
			t.Fatal(err)
		}
		if result := <-resultChannel; !result.TimedOut {
			t.Fatalf("limit probe result = %#v", result)
		}
	})

	t.Run("timeout and cleanup", func(t *testing.T) {
		limits := DefaultLimits()
		limits.Timeout = time.Second
		result := runReal(t, limits, `package main
func main() { for {} }`)
		if !result.TimedOut || result.ExitCode == 0 {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("network and secrets isolated", func(t *testing.T) {
		limits := DefaultLimits()
		limits.Timeout = 20 * time.Second // The net package is compiled from a cold, disposable cache.
		result := runReal(t, limits, `package main
import (
  "fmt"
  "net"
  "os"
  "time"
)
func main() {
  connection, err := net.DialTimeout("tcp", "1.1.1.1:80", 500*time.Millisecond)
  if connection != nil { connection.Close() }
  fmt.Println("network-blocked", err != nil)
  fmt.Println("secrets-empty", os.Getenv("MYSQL_DSN") == "" && os.Getenv("JWT_SECRET") == "")
}`)
		if result.ExitCode != 0 || !strings.Contains(result.Stdout, "network-blocked true") || !strings.Contains(result.Stdout, "secrets-empty true") {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("output truncated", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxOutputBytes = 1024
		result := runReal(t, limits, `package main
import "fmt"
func main() { for i := 0; i < 10000; i++ { fmt.Println("012345678901234567890123456789") } }`)
		if !result.OutputTruncated || len(result.Stdout)+len(result.Stderr) != 1024 {
			t.Fatalf("result lengths stdout=%d stderr=%d truncated=%v", len(result.Stdout), len(result.Stderr), result.OutputTruncated)
		}
	})

	t.Run("host paths unavailable", func(t *testing.T) {
		result := runReal(t, DefaultLimits(), `package main
import (
  "fmt"
  "os"
)
func main() {
  _, err := os.ReadFile("/Users/wangjiawei/Desktop/代码管理平台/.env")
  fmt.Println("host-blocked", err != nil)
}`)
		if result.ExitCode != 0 || !strings.Contains(result.Stdout, "host-blocked true") {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("memory limit", func(t *testing.T) {
		result := runReal(t, DefaultLimits(), `package main
func main() {
  value := make([]byte, 1024*1024*1024)
  for i := range value { value[i] = 1 }
  println(len(value))
}`)
		if result.ExitCode == 0 {
			t.Fatalf("memory-constrained program unexpectedly succeeded: %#v", result)
		}
	})

	t.Run("concurrent runs isolated", func(t *testing.T) {
		var wait sync.WaitGroup
		results := make([]Result, 2)
		for index, marker := range []string{"alpha", "beta"} {
			wait.Add(1)
			go func(index int, marker string) {
				defer wait.Done()
				runner, err := NewDockerRunner(DefaultLimits())
				if err != nil {
					t.Errorf("create runner: %v", err)
					return
				}
				results[index], err = runner.Run(context.Background(), Request{Language: LanguageGo, Source: "package main\nfunc main(){println(\"" + marker + "\")}\n"})
				if err != nil {
					t.Errorf("run %s: %v", marker, err)
				}
			}(index, marker)
		}
		wait.Wait()
		if results[0].Stderr != "alpha\n" || results[1].Stderr != "beta\n" {
			t.Fatalf("results = %#v", results)
		}
	})

	output, err := exec.Command("docker", "ps", "-a", "--filter", "label=com.collab-editor.sandbox=true", "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("sandbox containers were not cleaned up: %s", output)
	}
}

func runReal(t *testing.T, limits Limits, source string) Result {
	t.Helper()
	runner, err := NewDockerRunner(limits)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Language: LanguageGo, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
