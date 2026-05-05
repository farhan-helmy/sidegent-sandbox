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
