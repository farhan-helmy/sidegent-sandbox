# Sidegent CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename binary to `sidegent`, add CLI subcommands (`serve`, `run`), embed Dockerfile for auto-setup on first run.

**Architecture:** Refactor current monolithic `main.go` into focused files: `main.go` (CLI dispatch), `serve.go` (HTTP server), `run.go` (one-shot execution), `setup.go` (auto image build). The executor and its tests are untouched.

**Tech Stack:** Go 1.25 stdlib `flag` package for CLI parsing, Go `embed` for baking Dockerfile into binary, Docker SDK `ImageBuild` for auto-setup.

---

## File Structure

```
simple-sandbox/
├── main.go              # CLI entry: parse subcommand, dispatch, --help, --version
├── serve.go             # serve subcommand: config, router, handlers, server lifecycle
├── run.go               # run subcommand: one-shot execution, print stdout/stderr, exit code
├── setup.go             # embed Dockerfile, ensureImage() auto-build
├── executor.go          # (unchanged)
├── serve_test.go        # HTTP handler tests (moved from main_test.go, updated image name)
├── run_test.go          # run subcommand tests
├── setup_test.go        # ensureImage tests
├── executor_test.go     # (updated image name only)
├── Dockerfile.sandbox   # (unchanged)
├── setup.sh             # (unchanged)
├── .gitignore           # (updated: sidegent instead of simple-sandbox)
├── go.mod
└── go.sum
```

**Responsibilities:**
- `main.go`: Parse `os.Args`, dispatch to `runServe()` or `runCode()`, print help/version. ~50 LOC.
- `serve.go`: All HTTP server code currently in main.go (config, router, handlers, graceful shutdown). Exported as `runServe(port, maxConcurrent, defaultTimeout, maxTimeout int) error`.
- `run.go`: Create executor, call `ensureImage`, execute code, print stdout to os.Stdout, stderr to os.Stderr, exit with process code. Exported as `runCode(code string, timeout int) error`.
- `setup.go`: `//go:embed Dockerfile.sandbox`, `ensureImage(ctx, imageName string) error` that checks Docker for image and builds if missing.
- `executor.go`: Untouched (no changes).

---

### Task 1: Create setup.go with embedded Dockerfile and auto-build

**Files:**
- Create: `setup.go`
- Create: `setup_test.go`

- [ ] **Step 1: Write the test for ensureImage when image exists**

Create `setup_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"
)

func TestEnsureImageAlreadyExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// sidegent-python should already exist (built previously as simple-sandbox-python,
	// we'll tag it in the test). For now, test with any image that exists.
	err := ensureImage(ctx, "python:3.12-slim")
	if err != nil {
		t.Fatalf("ensureImage for existing image: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestEnsureImageAlreadyExists -short=false -timeout 60s
```

Expected: FAIL — `ensureImage` not defined.

- [ ] **Step 3: Implement setup.go**

Create `setup.go`:

```go
package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

//go:embed Dockerfile.sandbox
var dockerfileContent []byte

// ensureImage checks if the Docker image exists locally. If not, it builds it
// from the embedded Dockerfile. Returns nil if the image is ready to use.
func ensureImage(ctx context.Context, imageName string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("docker client: %w (is Docker running?)", err)
	}
	defer cli.Close()

	// Check if image already exists
	_, _, err = cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return nil // image exists
	}

	fmt.Fprintf(os.Stderr, "Building sandbox image (first run only)...\n")

	// Create a tar archive with just the Dockerfile
	tarBuf := new(bytes.Buffer)
	header := archive.GenerateHeader("Dockerfile", len(dockerfileContent))
	tw := tar.NewWriter(tarBuf)
	tw.WriteHeader(header)
	tw.Write(dockerfileContent)
	tw.Close()

	resp, err := cli.ImageBuild(ctx, tarBuf, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{imageName},
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	// Drain build output (required for build to complete)
	io.Copy(io.Discard, resp.Body)

	fmt.Fprintf(os.Stderr, "Sandbox image ready.\n")
	return nil
}
```

**NOTE to implementer:** The tar archive creation above uses a simplified approach. The Docker SDK's `archive` package may not have `GenerateHeader`. Instead, use `archive/tar` from the Go stdlib directly:

```go
import "archive/tar"

tarBuf := new(bytes.Buffer)
tw := tar.NewWriter(tarBuf)
tw.WriteHeader(&tar.Header{
    Name: "Dockerfile",
    Size: int64(len(dockerfileContent)),
    Mode: 0644,
})
tw.Write(dockerfileContent)
tw.Close()
```

And for `types.ImageBuildOptions`, the correct import may be `github.com/docker/docker/api/types` directly. Check the Docker SDK version in go.mod (v28.0.1) and use the correct types. Run `go mod tidy` if needed.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestEnsureImageAlreadyExists -timeout 60s
```

Expected: PASS — image already exists, returns nil immediately.

- [ ] **Step 5: Write test for building a missing image**

Add to `setup_test.go`:

```go
func TestEnsureImageBuildsWhenMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Use a unique tag so we know it doesn't exist
	testImage := "sidegent-python-test"

	// Remove image if it exists from a previous test run
	cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if cli != nil {
		cli.ImageRemove(ctx, testImage, image.RemoveOptions{Force: true})
		cli.Close()
	}

	err := ensureImage(ctx, testImage)
	if err != nil {
		t.Fatalf("ensureImage for missing image: %v", err)
	}

	// Verify image now exists
	cli2, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	defer cli2.Close()
	_, _, err = cli2.ImageInspectWithRaw(ctx, testImage)
	if err != nil {
		t.Fatalf("image should exist after build: %v", err)
	}

	// Cleanup
	cli2.ImageRemove(ctx, testImage, image.RemoveOptions{Force: true})
}
```

Add the import for `"github.com/docker/docker/api/types/image"` and `"github.com/docker/docker/client"` to the test file.

- [ ] **Step 6: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestEnsureImageBuildsWhenMissing -timeout 300s
```

Expected: PASS — builds image from embedded Dockerfile (may take 1-2 min on first run).

- [ ] **Step 7: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add setup.go setup_test.go
git commit -m "feat: add auto-setup with embedded Dockerfile and image build"
```

---

### Task 2: Extract serve.go from main.go

**Files:**
- Create: `serve.go`
- Rename: `main_test.go` → `serve_test.go`
- Modify: `main.go` (strip down to just a placeholder main that calls runServe)

This task moves all HTTP server code out of main.go into serve.go. The main.go is temporarily simplified to just call runServe — it will be replaced with the CLI dispatcher in Task 4.

- [ ] **Step 1: Create serve.go with all HTTP server code**

Create `serve.go` — this is the entire content of the current main.go EXCEPT the `main()` function, with `runServe` added:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const defaultImage = "sidegent-python"

type Config struct {
	Port           int
	MaxConcurrent  int
	DefaultTimeout int
	MaxTimeout     int
	SandboxImage   string
	SandboxMemory  string
	SandboxCPUs    int
}

func defaultConfig() Config {
	return Config{
		Port:           8080,
		MaxConcurrent:  10,
		DefaultTimeout: 30,
		MaxTimeout:     120,
		SandboxImage:   defaultImage,
		SandboxMemory:  "512m",
		SandboxCPUs:    1,
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func parseMemory(s string) int64 {
	if len(s) == 0 {
		return 512 * 1024 * 1024
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 512 * 1024 * 1024
	}
	switch unit {
	case 'm', 'M':
		return num * 1024 * 1024
	case 'g', 'G':
		return num * 1024 * 1024 * 1024
	default:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		return 512 * 1024 * 1024
	}
}

type executeRequest struct {
	Code    string `json:"code"`
	Timeout int    `json:"timeout,omitempty"`
}

type executeResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func newRouter(exec *Executor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /execute", handleExecute(exec))
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleExecute(exec *Executor) http.HandlerFunc {
	cfg := defaultConfig()
	cfg.DefaultTimeout = envIntOrDefault("DEFAULT_TIMEOUT", cfg.DefaultTimeout)
	cfg.MaxTimeout = envIntOrDefault("MAX_TIMEOUT", cfg.MaxTimeout)

	return func(w http.ResponseWriter, r *http.Request) {
		var req executeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON"})
			return
		}

		if req.Code == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "code is required"})
			return
		}

		timeout := cfg.DefaultTimeout
		if req.Timeout > 0 {
			timeout = req.Timeout
		}
		if timeout > cfg.MaxTimeout {
			timeout = cfg.MaxTimeout
		}

		if !exec.TryAcquire() {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "too many concurrent executions"})
			return
		}
		defer exec.Release()

		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
		defer cancel()

		result, err := exec.Execute(ctx, req.Code)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				writeJSON(w, http.StatusRequestTimeout, errorResponse{
					Error: fmt.Sprintf("execution timed out after %ds", timeout),
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "execution failed"})
			log.Printf("execute error: %v", err)
			return
		}

		writeJSON(w, http.StatusOK, executeResponse{
			Stdout:     result.Stdout,
			Stderr:     result.Stderr,
			ExitCode:   result.ExitCode,
			DurationMs: result.DurationMs,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func runServe(port, maxConcurrent, defaultTimeout, maxTimeout int) error {
	image := envOrDefault("SANDBOX_IMAGE", defaultImage)
	memory := envOrDefault("SANDBOX_MEMORY", "512m")
	cpus := envIntOrDefault("SANDBOX_CPUS", 1)

	// Auto-setup: ensure sandbox image exists
	ctx := context.Background()
	if err := ensureImage(ctx, image); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         image,
		Memory:        parseMemory(memory),
		CPUs:          int64(cpus),
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: maxConcurrent,
	})
	if err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	defer exec.Close()

	router := newRouter(exec)
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: router,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("sidegent listening on :%d", port)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-done
	log.Println("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	return server.Shutdown(shutdownCtx)
}
```

- [ ] **Step 2: Rename main_test.go to serve_test.go**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && git mv main_test.go serve_test.go
```

- [ ] **Step 3: Update image name in serve_test.go**

In `serve_test.go`, change all occurrences of `"simple-sandbox-python"` to `"sidegent-python"`. There are 2 occurrences (in TestExecuteIntegration and TestExecuteConcurrencyLimit).

- [ ] **Step 4: Update image name in executor_test.go**

In `executor_test.go`, change all occurrences of `"simple-sandbox-python"` to `"sidegent-python"`. There are 4 occurrences.

- [ ] **Step 5: Replace main.go with minimal placeholder**

Replace the entire content of `main.go` with:

```go
package main

import (
	"log"
)

func main() {
	if err := runServe(8080, 10, 30, 120); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 6: Tag the existing Docker image with new name**

```bash
docker tag simple-sandbox-python sidegent-python
```

- [ ] **Step 7: Run all tests to verify nothing broke**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -timeout 120s ./...
```

Expected: all 9 tests PASS (same tests, just moved file + new image name).

- [ ] **Step 8: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add main.go serve.go serve_test.go executor_test.go
git commit -m "refactor: extract serve.go from main.go, rename image to sidegent-python"
```

---

### Task 3: Implement run.go (one-shot execution)

**Files:**
- Create: `run.go`
- Create: `run_test.go`

- [ ] **Step 1: Write the test for runCode**

Create `run_test.go`:

```go
package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestRunCodeBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	exitCode := runCode("print('hello from run')", 30)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
	if buf.String() != "hello from run\n" {
		t.Errorf("stdout = %q, want %q", buf.String(), "hello from run\n")
	}
}

func TestRunCodeError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	exitCode := runCode("raise ValueError('bad')", 30)

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)

	if exitCode == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if buf.String() == "" {
		t.Errorf("stderr is empty, want traceback")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestRunCode -timeout 60s
```

Expected: FAIL — `runCode` not defined.

- [ ] **Step 3: Implement run.go**

Create `run.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// runCode executes Python code in a sandbox and prints output directly.
// Returns the exit code from the Python process.
func runCode(code string, timeout int) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	image := envOrDefault("SANDBOX_IMAGE", defaultImage)

	if err := ensureImage(ctx, image); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         image,
		Memory:        parseMemory(envOrDefault("SANDBOX_MEMORY", "512m")),
		CPUs:          int64(envIntOrDefault("SANDBOX_CPUS", 1)),
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 1,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer exec.Close()

	result, err := exec.Execute(ctx, code)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "Error: execution timed out after %ds\n", timeout)
			return 124 // standard timeout exit code
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	if result.Stdout != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}

	return result.ExitCode
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestRunCode -timeout 120s
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add run.go run_test.go
git commit -m "feat: add run subcommand for one-shot code execution"
```

---

### Task 4: Implement CLI dispatcher in main.go

**Files:**
- Modify: `main.go` (replace placeholder with full CLI)
- Modify: `.gitignore` (update binary name)

- [ ] **Step 1: Replace main.go with CLI dispatcher**

Replace the entire content of `main.go` with:

```go
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
		port := serveCmd.Int("port", 8080, "HTTP server port")
		maxConcurrent := serveCmd.Int("max-concurrent", 10, "Max concurrent executions")
		timeout := serveCmd.Int("timeout", 30, "Default execution timeout (seconds)")
		maxTimeout := serveCmd.Int("max-timeout", 120, "Maximum allowed timeout (seconds)")
		serveCmd.Parse(os.Args[2:])

		if err := runServe(*port, *maxConcurrent, *timeout, *maxTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "run":
		runCmd := flag.NewFlagSet("run", flag.ExitOnError)
		timeout := runCmd.Int("timeout", 30, "Execution timeout (seconds)")
		runCmd.Parse(os.Args[2:])

		args := runCmd.Args()
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "Error: code argument required\nUsage: sidegent run [--timeout N] 'code'\n")
			os.Exit(1)
		}

		code := args[0]
		os.Exit(runCode(code, *timeout))

	case "--version", "-v", "version":
		fmt.Printf("sidegent v%s\n", version)

	case "--help", "-h", "help":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`sidegent - Run Python code in secure sandboxes

Usage:
  sidegent <command> [flags]

Commands:
  serve   Start the HTTP API server
  run     Execute Python code in a sandbox

Examples:
  sidegent run 'print(42)'
  sidegent run --timeout 10 'import time; time.sleep(5)'
  sidegent serve
  sidegent serve --port 3000

Flags:
  --help      Show help
  --version   Show version
`)
}
```

- [ ] **Step 2: Update .gitignore**

Replace `.gitignore` content with:

```
sidegent
```

- [ ] **Step 3: Verify build**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go build -o sidegent .
```

Expected: builds without errors.

- [ ] **Step 4: Test CLI help and version**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
./sidegent --help
./sidegent --version
```

Expected: help text and `sidegent v0.1.0`.

- [ ] **Step 5: Test run subcommand**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && ./sidegent run 'print(42)'
```

Expected: prints `42`.

- [ ] **Step 6: Test serve subcommand**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && ./sidegent serve &
sleep 2
curl -s http://localhost:8080/health
curl -s -X POST http://localhost:8080/execute -H "Content-Type: application/json" -d '{"code":"print(99)"}'
kill %1
```

Expected: health returns ok, execute returns `{"stdout":"99\n",...}`.

- [ ] **Step 7: Run all tests**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -timeout 120s ./...
```

Expected: all tests PASS.

- [ ] **Step 8: Clean up binary and commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
rm -f sidegent
git add main.go .gitignore
git commit -m "feat: add CLI with serve and run subcommands"
```

---

### Task 5: Final verification

- [ ] **Step 1: Run full test suite**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -timeout 120s ./...
```

Expected: all tests PASS (should be ~11+ tests now).

- [ ] **Step 2: Build and smoke test the full CLI**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go build -o sidegent .

# Version
./sidegent --version

# Help
./sidegent --help

# Run one-shot
./sidegent run 'print("hello sidegent")'

# Run with numpy
./sidegent run 'import numpy as np; print(np.array([1,2,3]).sum())'

# Run error
./sidegent run 'raise Exception("test")'

# Run missing code
./sidegent run
```

Expected: all behave correctly.

- [ ] **Step 3: Clean up and commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
rm -f sidegent
git status
```

Expected: clean working tree.
