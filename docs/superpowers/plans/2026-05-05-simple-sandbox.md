# Simple Sandbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a single Go binary that accepts Python code via HTTP, runs it in a gVisor-isolated Docker container, and returns stdout/stderr.

**Architecture:** HTTP server (net/http stdlib) receives code, passes it to an executor that uses the Docker SDK to create a short-lived container with gVisor runtime, no network, strict resource limits. Container runs the code, output is collected, container is removed.

**Tech Stack:** Go 1.25, Docker SDK (`github.com/docker/docker`), gVisor (`runsc` runtime), Python 3.12 (sandbox image)

---

## File Structure

```
simple-sandbox/
├── main.go              # Config loading, HTTP routes, server lifecycle
├── executor.go          # Docker SDK: create, run, collect output, remove container
├── executor_test.go     # Integration tests for executor (requires Docker)
├── main_test.go         # HTTP handler tests
├── Dockerfile.sandbox   # Python sandbox image
├── go.mod
├── go.sum
├── setup.sh             # EC2 setup script
└── docs/                # (already exists)
```

**Responsibilities:**
- `main.go`: Parse config from env vars, wire up HTTP routes (`/execute`, `/health`), graceful shutdown
- `executor.go`: All Docker SDK interaction — create container, copy code, start, wait, collect logs, remove. Exposes a single `Executor` struct with an `Execute(ctx, code string) (Result, error)` method
- `executor_test.go`: Integration tests that actually run containers (need Docker running)
- `main_test.go`: HTTP-level tests using httptest, with a mock executor

---

### Task 1: Initialize Go module and dependencies

**Files:**
- Create: `go.mod`
- Create: `go.sum`

- [ ] **Step 1: Initialize Go module**

Run from `/Users/farhan/dev/opensos/simple-sandbox`:

```bash
go mod init github.com/farhan/simple-sandbox
```

- [ ] **Step 2: Add Docker SDK dependency**

```bash
go get github.com/docker/docker/client@v28.0.1+incompatible
go get github.com/docker/docker/api/types@v28.0.1+incompatible
go get github.com/docker/go-connections@v0.5.0
```

- [ ] **Step 3: Tidy dependencies**

```bash
go mod tidy
```

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: init go module with docker sdk dependency"
```

---

### Task 2: Build the Python sandbox Docker image

**Files:**
- Create: `Dockerfile.sandbox`

- [ ] **Step 1: Create the Dockerfile**

Create `Dockerfile.sandbox`:

```dockerfile
FROM python:3.12-slim

RUN pip install --no-cache-dir \
    numpy \
    pandas \
    matplotlib \
    scipy \
    sympy

# No CMD — entrypoint is provided at container creation time
```

Note: `requests` is excluded because the sandbox is air-gapped (no network). Including it would be misleading.

- [ ] **Step 2: Build the image**

```bash
docker build -t simple-sandbox-python -f Dockerfile.sandbox .
```

Expected: image builds successfully, ~500MB.

- [ ] **Step 3: Verify the image works**

```bash
docker run --rm simple-sandbox-python python -c "import numpy; print(numpy.__version__)"
```

Expected: prints numpy version (e.g., `2.2.5`).

- [ ] **Step 4: Commit**

```bash
git add Dockerfile.sandbox
git commit -m "feat: add python sandbox docker image"
```

---

### Task 3: Implement the executor

**Files:**
- Create: `executor.go`
- Create: `executor_test.go`

- [ ] **Step 1: Write the failing test for basic code execution**

Create `executor_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"
)

func TestExecuteSimplePrint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "simple-sandbox-python",
		Memory:        512 * 1024 * 1024, // 512MB
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024, // 64MB
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	defer exec.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := exec.Execute(ctx, `print("hello sandbox")`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}
	if result.Stdout != "hello sandbox\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello sandbox\n")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteSimplePrint
```

Expected: FAIL — `NewExecutor` not defined.

- [ ] **Step 3: Implement the executor**

Create `executor.go`:

```go
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type ExecutorConfig struct {
	Image         string
	Memory        int64 // bytes
	CPUs          int64 // number of CPUs
	PidsLimit     int64
	TmpfsSize     int64 // bytes
	MaxConcurrent int
}

type Result struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

type Executor struct {
	client *client.Client
	config ExecutorConfig
	sem    chan struct{}
}

func NewExecutor(cfg ExecutorConfig) (*Executor, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	return &Executor{
		client: cli,
		config: cfg,
		sem:    make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

func (e *Executor) Close() error {
	return e.client.Close()
}

// TryAcquire attempts to acquire a semaphore slot without blocking.
// Returns false if all slots are in use.
func (e *Executor) TryAcquire() bool {
	select {
	case e.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (e *Executor) Release() {
	<-e.sem
}

func (e *Executor) Execute(ctx context.Context, code string) (Result, error) {
	start := time.Now()

	// Detect gVisor availability — fall back to default runtime if not installed.
	runtime := "runsc"
	info, err := e.client.Info(ctx)
	if err == nil {
		if _, ok := info.Runtimes["runsc"]; !ok {
			runtime = info.DefaultRuntime
		}
	}

	nanoCPUs := e.config.CPUs * 1e9

	resp, err := e.client.ContainerCreate(ctx,
		&container.Config{
			Image: e.config.Image,
			Cmd:   []string{"python", "-c", code},
			// Disable networking at container config level too
			NetworkDisabled: true,
		},
		&container.HostConfig{
			Runtime: runtime,
			Resources: container.Resources{
				Memory:   e.config.Memory,
				NanoCPUs: nanoCPUs,
				PidsLimit: &e.config.PidsLimit,
			},
			NetworkMode: "none",
			ReadonlyRootfs: true,
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeTmpfs,
					Target: "/tmp",
					TmpfsOptions: &mount.TmpfsOptions{
						SizeBytes: e.config.TmpfsSize,
					},
				},
			},
		},
		nil, // networking config
		nil, // platform
		"",  // auto-generate name
	)
	if err != nil {
		return Result{}, fmt.Errorf("container create: %w", err)
	}

	containerID := resp.ID
	// Always clean up the container
	defer func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		e.client.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true})
	}()

	if err := e.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return Result{}, fmt.Errorf("container start: %w", err)
	}

	// Wait for container to exit or context to cancel (timeout)
	waitCh, errCh := e.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	var exitCode int
	select {
	case waitResult := <-waitCh:
		exitCode = int(waitResult.StatusCode)
	case waitErr := <-errCh:
		if ctx.Err() != nil {
			// Timeout — kill the container
			killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer killCancel()
			e.client.ContainerKill(killCtx, containerID, "KILL")
			return Result{DurationMs: time.Since(start).Milliseconds()}, ctx.Err()
		}
		return Result{}, fmt.Errorf("container wait: %w", waitErr)
	}

	// Collect logs
	logReader, err := e.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return Result{ExitCode: exitCode, DurationMs: time.Since(start).Milliseconds()}, fmt.Errorf("container logs: %w", err)
	}
	defer logReader.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	// Docker multiplexes stdout/stderr into a single stream with headers.
	// stdcopy.StdCopy demultiplexes them.
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, logReader); err != nil {
		return Result{ExitCode: exitCode, DurationMs: time.Since(start).Milliseconds()}, fmt.Errorf("reading logs: %w", err)
	}

	return Result{
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   exitCode,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// Ensure io.Reader is satisfied (compile-time check for stdcopy usage)
var _ io.Reader = (*bytes.Buffer)(nil)
var _ sync.Locker = (*sync.Mutex)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteSimplePrint
```

Expected: PASS — prints "hello sandbox".

- [ ] **Step 5: Write test for stderr output**

Add to `executor_test.go`:

```go
func TestExecuteStderr(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "simple-sandbox-python",
		Memory:        512 * 1024 * 1024,
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	defer exec.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := exec.Execute(ctx, `import sys; sys.stderr.write("oops\n")`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Stderr != "oops\n" {
		t.Errorf("stderr = %q, want %q", result.Stderr, "oops\n")
	}
}
```

- [ ] **Step 6: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteStderr
```

Expected: PASS.

- [ ] **Step 7: Write test for non-zero exit code**

Add to `executor_test.go`:

```go
func TestExecuteExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "simple-sandbox-python",
		Memory:        512 * 1024 * 1024,
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	defer exec.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := exec.Execute(ctx, `raise ValueError("bad")`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if result.Stderr == "" {
		t.Errorf("stderr is empty, want error traceback")
	}
}
```

- [ ] **Step 8: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteExitCode
```

Expected: PASS.

- [ ] **Step 9: Write test for timeout**

Add to `executor_test.go`:

```go
func TestExecuteTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "simple-sandbox-python",
		Memory:        512 * 1024 * 1024,
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	defer exec.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = exec.Execute(ctx, `import time; time.sleep(60)`)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("context error = %v, want DeadlineExceeded", ctx.Err())
	}
}
```

- [ ] **Step 10: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteTimeout -timeout 30s
```

Expected: PASS — should complete in ~3 seconds, not 60.

- [ ] **Step 11: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add executor.go executor_test.go
git commit -m "feat: implement docker executor with container lifecycle"
```

---

### Task 4: Implement the HTTP server

**Files:**
- Create: `main.go`
- Create: `main_test.go`

- [ ] **Step 1: Write the failing test for the health endpoint**

Create `main_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	handler := newRouter(nil) // nil executor — health doesn't need it
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestHealthEndpoint -short
```

Expected: FAIL — `newRouter` not defined.

- [ ] **Step 3: Implement main.go with config, router, and handlers**

Create `main.go`:

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

type Config struct {
	Port           string
	MaxConcurrent  int
	DefaultTimeout int
	MaxTimeout     int
	SandboxImage   string
	SandboxMemory  string
	SandboxCPUs    int
}

func loadConfig() Config {
	return Config{
		Port:           envOrDefault("PORT", "8080"),
		MaxConcurrent:  envIntOrDefault("MAX_CONCURRENT", 10),
		DefaultTimeout: envIntOrDefault("DEFAULT_TIMEOUT", 30),
		MaxTimeout:     envIntOrDefault("MAX_TIMEOUT", 120),
		SandboxImage:   envOrDefault("SANDBOX_IMAGE", "simple-sandbox-python"),
		SandboxMemory:  envOrDefault("SANDBOX_MEMORY", "512m"),
		SandboxCPUs:    envIntOrDefault("SANDBOX_CPUS", 1),
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
	// Supports "512m" or "1g" shorthand
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
		// Try parsing the whole string as bytes
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
	cfg := loadConfig()

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

func main() {
	cfg := loadConfig()

	exec, err := NewExecutor(ExecutorConfig{
		Image:         cfg.SandboxImage,
		Memory:        parseMemory(cfg.SandboxMemory),
		CPUs:          int64(cfg.SandboxCPUs),
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: cfg.MaxConcurrent,
	})
	if err != nil {
		log.Fatalf("failed to create executor: %v", err)
	}
	defer exec.Close()

	router := newRouter(exec)
	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("simple-sandbox listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-done
	log.Println("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
}
```

- [ ] **Step 4: Run health test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestHealthEndpoint -short
```

Expected: PASS.

- [ ] **Step 5: Write test for /execute with missing code**

Add to `main_test.go`:

```go
import (
	"encoding/json"
	"strings"
)

func TestExecuteMissingCode(t *testing.T) {
	handler := newRouter(nil)
	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "code is required" {
		t.Errorf("error = %q, want %q", resp.Error, "code is required")
	}
}
```

Note: the import block at the top of the file should be merged with the existing one — add `"encoding/json"` and `"strings"` to the existing import.

- [ ] **Step 6: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteMissingCode -short
```

Expected: PASS.

- [ ] **Step 7: Write test for /execute with invalid JSON**

Add to `main_test.go`:

```go
func TestExecuteInvalidJSON(t *testing.T) {
	handler := newRouter(nil)
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
```

- [ ] **Step 8: Run test to verify it passes**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteInvalidJSON -short
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add main.go main_test.go
git commit -m "feat: add http server with /execute and /health endpoints"
```

---

### Task 5: End-to-end integration test

**Files:**
- Modify: `main_test.go`

This test exercises the full stack: HTTP handler → executor → Docker container → response.

- [ ] **Step 1: Write the integration test**

Add to `main_test.go`:

```go
func TestExecuteIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "simple-sandbox-python",
		Memory:        512 * 1024 * 1024,
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	defer exec.Close()

	handler := newRouter(exec)
	body := strings.NewReader(`{"code": "print(2 + 2)", "timeout": 30}`)
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp executeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Stdout != "4\n" {
		t.Errorf("stdout = %q, want %q", resp.Stdout, "4\n")
	}
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", resp.ExitCode)
	}
	if resp.DurationMs <= 0 {
		t.Errorf("duration_ms = %d, want > 0", resp.DurationMs)
	}
}
```

- [ ] **Step 2: Run it**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteIntegration -timeout 60s
```

Expected: PASS — full round-trip: HTTP → Docker → Python → response.

- [ ] **Step 3: Write integration test for concurrency limit (503)**

Add to `main_test.go`:

```go
import "sync"

func TestExecuteConcurrencyLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "simple-sandbox-python",
		Memory:        512 * 1024 * 1024,
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 1, // Only allow 1 concurrent execution
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	defer exec.Close()

	handler := newRouter(exec)

	// Fill the semaphore with a long-running request
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		body := strings.NewReader(`{"code": "import time; time.sleep(5)"}`)
		req := httptest.NewRequest(http.MethodPost, "/execute", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}()

	// Give the first request time to acquire the semaphore
	time.Sleep(500 * time.Millisecond)

	// Second request should get 503
	body := strings.NewReader(`{"code": "print(1)"}`)
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	wg.Wait()
}
```

Note: add `"sync"` and `"time"` to the import block if not already present.

- [ ] **Step 4: Run it**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -run TestExecuteConcurrencyLimit -timeout 30s
```

Expected: PASS.

- [ ] **Step 5: Run all tests together**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -timeout 120s ./...
```

Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add main_test.go
git commit -m "test: add integration tests for full execute flow and concurrency"
```

---

### Task 6: EC2 setup script

**Files:**
- Create: `setup.sh`

- [ ] **Step 1: Create the setup script**

Create `setup.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

echo "=== Simple Sandbox EC2 Setup ==="

# Install Docker
if ! command -v docker &>/dev/null; then
    echo "Installing Docker..."
    curl -fsSL https://get.docker.com | sh
    sudo usermod -aG docker "$USER"
    echo "Docker installed. You may need to log out and back in for group changes."
fi

# Install gVisor (runsc)
if ! command -v runsc &>/dev/null; then
    echo "Installing gVisor..."
    ARCH=$(uname -m)
    if [ "$ARCH" = "x86_64" ]; then
        URL=https://storage.googleapis.com/gvisor/releases/release/latest/x86_64
    elif [ "$ARCH" = "aarch64" ]; then
        URL=https://storage.googleapis.com/gvisor/releases/release/latest/aarch64
    else
        echo "Unsupported architecture: $ARCH"
        exit 1
    fi
    wget "${URL}/runsc" "${URL}/containerd-shim-runsc-v1" -P /tmp/
    sudo install -m 755 /tmp/runsc /usr/local/bin/
    sudo install -m 755 /tmp/containerd-shim-runsc-v1 /usr/local/bin/
    rm -f /tmp/runsc /tmp/containerd-shim-runsc-v1
fi

# Configure Docker to use runsc runtime
if ! docker info 2>/dev/null | grep -q runsc; then
    echo "Configuring Docker with gVisor runtime..."
    sudo mkdir -p /etc/docker
    cat <<DOCKEREOF | sudo tee /etc/docker/daemon.json
{
    "runtimes": {
        "runsc": {
            "path": "/usr/local/bin/runsc"
        }
    }
}
DOCKEREOF
    sudo systemctl restart docker
fi

# Build the sandbox Python image
echo "Building sandbox Python image..."
docker build -t simple-sandbox-python -f Dockerfile.sandbox .

# Build the Go binary
echo "Building simple-sandbox..."
if ! command -v go &>/dev/null; then
    echo "Go not found. Install Go 1.21+ first: https://go.dev/dl/"
    exit 1
fi
go build -o simple-sandbox .

echo ""
echo "=== Setup complete ==="
echo "Run: ./simple-sandbox"
echo "Test: curl -X POST http://localhost:8080/execute -d '{\"code\": \"print(42)\"}'"
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x /Users/farhan/dev/opensos/simple-sandbox/setup.sh
```

- [ ] **Step 3: Commit**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add setup.sh
git commit -m "feat: add ec2 setup script for docker and gvisor"
```

---

### Task 7: Final verification

- [ ] **Step 1: Run all tests**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go test -v -timeout 120s ./...
```

Expected: all tests PASS.

- [ ] **Step 2: Build the binary**

```bash
cd /Users/farhan/dev/opensos/simple-sandbox && go build -o simple-sandbox .
```

Expected: produces `simple-sandbox` binary with no errors.

- [ ] **Step 3: Manual smoke test**

In one terminal:
```bash
cd /Users/farhan/dev/opensos/simple-sandbox && ./simple-sandbox
```

In another terminal:
```bash
# Health check
curl -s http://localhost:8080/health | jq .

# Execute code
curl -s -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{"code": "print(42)"}' | jq .

# Execute with imports
curl -s -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{"code": "import numpy as np\nprint(np.array([1,2,3]).sum())"}' | jq .
```

Expected: health returns `{"status":"ok"}`, execute returns stdout with correct output.

- [ ] **Step 4: Clean up binary from repo**

```bash
rm /Users/farhan/dev/opensos/simple-sandbox/simple-sandbox
```

- [ ] **Step 5: Add .gitignore and commit**

Create `.gitignore`:
```
simple-sandbox
```

```bash
cd /Users/farhan/dev/opensos/simple-sandbox
git add .gitignore
git commit -m "chore: add gitignore for compiled binary"
```
