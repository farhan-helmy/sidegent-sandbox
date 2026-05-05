package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// runCode executes a Python code snippet in a sandboxed container and returns
// the process exit code. Stdout and stderr from the execution are written
// directly to os.Stdout and os.Stderr respectively.
//
// Exit codes:
//
//	0   - success
//	1   - general error (docker failure, execution error)
//	124 - timeout exceeded
func runCode(code string, timeout int) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	image := envOrDefault("SANDBOX_IMAGE", defaultImage)

	if err := ensureImage(ctx, image); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to ensure image: %v\n", err)
		return 1
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         image,
		Memory:        512 * 1024 * 1024,
		CPUs:          1,
		PidsLimit:     64,
		TmpfsSize:     64 * 1024 * 1024,
		MaxConcurrent: 1,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create executor: %v\n", err)
		return 1
	}
	defer exec.Close()

	result, err := exec.Execute(ctx, code)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "error: execution timed out after %ds\n", timeout)
			return 124
		}
		fmt.Fprintf(os.Stderr, "error: execution failed: %v\n", err)
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
