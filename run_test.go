package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunCodeBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Capture os.Stdout via a pipe.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	exitCode := runCode(`print('hello from run')`, 30)

	// Close the write end and restore stdout before reading.
	w.Close()
	os.Stdout = origStdout

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	r.Close()
	output := string(buf[:n])

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(output, "hello from run") {
		t.Errorf("stdout = %q, want it to contain %q", output, "hello from run")
	}
}

func TestRunCodeError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Capture os.Stderr via a pipe.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	exitCode := runCode(`raise ValueError('bad')`, 30)

	// Close the write end and restore stderr before reading.
	w.Close()
	os.Stderr = origStderr

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	r.Close()
	output := string(buf[:n])

	if exitCode == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if output == "" {
		t.Errorf("stderr is empty, want error output")
	}
}
