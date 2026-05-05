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

func newRouter(exec *Executor, defaultTimeout, maxTimeout int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /execute", handleExecute(exec, defaultTimeout, maxTimeout))
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleExecute(exec *Executor, defaultTimeout, maxTimeout int) http.HandlerFunc {
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

		timeout := defaultTimeout
		if req.Timeout > 0 {
			timeout = req.Timeout
		}
		if timeout > maxTimeout {
			timeout = maxTimeout
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

	ctx := context.Background()
	if err := ensureImage(ctx, image); err != nil {
		return fmt.Errorf("ensure image: %w", err)
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
		return fmt.Errorf("failed to create executor: %w", err)
	}
	defer exec.Close()

	router := newRouter(exec, defaultTimeout, maxTimeout)
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
	server.Shutdown(shutdownCtx)

	return nil
}
