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
			Image:           e.config.Image,
			Cmd:             []string{"python", "-c", code},
			NetworkDisabled: true,
		},
		&container.HostConfig{
			Runtime: runtime,
			Resources: container.Resources{
				Memory:    e.config.Memory,
				NanoCPUs:  nanoCPUs,
				PidsLimit: &e.config.PidsLimit,
			},
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
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
		nil, nil, "",
	)
	if err != nil {
		return Result{}, fmt.Errorf("container create: %w", err)
	}

	containerID := resp.ID
	defer func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		e.client.ContainerRemove(removeCtx, containerID, container.RemoveOptions{Force: true})
	}()

	if err := e.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return Result{}, fmt.Errorf("container start: %w", err)
	}

	waitCh, errCh := e.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	var exitCode int
	select {
	case waitResult := <-waitCh:
		exitCode = int(waitResult.StatusCode)
	case waitErr := <-errCh:
		if ctx.Err() != nil {
			killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer killCancel()
			e.client.ContainerKill(killCtx, containerID, "KILL")
			return Result{DurationMs: time.Since(start).Milliseconds()}, ctx.Err()
		}
		return Result{}, fmt.Errorf("container wait: %w", waitErr)
	}

	logReader, err := e.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return Result{ExitCode: exitCode, DurationMs: time.Since(start).Milliseconds()}, fmt.Errorf("container logs: %w", err)
	}
	defer logReader.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
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

var _ io.Reader = (*bytes.Buffer)(nil)
var _ sync.Locker = (*sync.Mutex)(nil)
