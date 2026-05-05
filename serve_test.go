package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHealthEndpoint(t *testing.T) {
	handler := newRouter(nil, 30, 120)
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

func TestExecuteMissingCode(t *testing.T) {
	handler := newRouter(nil, 30, 120)
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

func TestExecuteInvalidJSON(t *testing.T) {
	handler := newRouter(nil, 30, 120)
	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestExecuteIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "sidegent-python",
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

	handler := newRouter(exec, 30, 120)
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

func TestExecuteConcurrencyLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	exec, err := NewExecutor(ExecutorConfig{
		Image:         "sidegent-python",
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

	handler := newRouter(exec, 30, 120)

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
