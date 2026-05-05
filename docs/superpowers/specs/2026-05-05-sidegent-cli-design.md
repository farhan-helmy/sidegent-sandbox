# Sidegent CLI Design

## Overview

Rename the binary to `sidegent`, add CLI subcommands (`serve`, `run`), embed the Dockerfile into the binary, and auto-build the Docker image on first run. Makes the tool a fully self-contained, installable CLI binary.

## Subcommands

### `sidegent serve`

Start the HTTP API server (existing behavior, wrapped in a subcommand).

```
sidegent serve              # Listen on :8080 (default)
sidegent serve --port 3000  # Custom port
```

Flags:
- `--port` (int, default 8080): HTTP server port
- `--max-concurrent` (int, default 10): Max concurrent executions
- `--timeout` (int, default 30): Default execution timeout in seconds
- `--max-timeout` (int, default 120): Maximum allowed timeout in seconds

### `sidegent run`

One-shot code execution from the terminal. Runs code, prints output, exits.

```
sidegent run 'print(42)'
sidegent run 'print(42)' --timeout 10
sidegent run 'import numpy; print(numpy.__version__)'
```

Flags:
- `--timeout` (int, default 30): Execution timeout in seconds

Output behavior:
- Print stdout directly to os.Stdout (no JSON wrapping)
- Print stderr directly to os.Stderr
- Exit with the same exit code as the Python process
- This makes `sidegent run` composable with other CLI tools and piping

### `sidegent --help`

```
sidegent - Run Python code in secure sandboxes

Usage:
  sidegent <command> [flags]

Commands:
  serve   Start the HTTP API server
  run     Execute Python code in a sandbox

Flags:
  --help      Show help
  --version   Show version
```

### `sidegent --version`

Prints version string, e.g., `sidegent v0.1.0`.

## Auto-Setup

On first run (either `serve` or `run`), before executing anything:

1. Check if Docker image `sidegent-python` exists locally (`docker image inspect`)
2. If missing, print `Building sandbox image (first run only)...`
3. Build the image from the embedded Dockerfile using Docker SDK's `ImageBuild` API
4. The `Dockerfile.sandbox` is embedded into the binary via Go's `//go:embed` directive
5. After build completes, proceed with the command

If Docker is not running, print a clear error: `Error: Docker is not running. Please start Docker and try again.`

## Project Structure Changes

### Before (current)

```
simple-sandbox/
├── main.go           # HTTP server + main()
├── executor.go       # Docker container lifecycle
├── executor_test.go
├── main_test.go
├── Dockerfile.sandbox
├── setup.sh
├── go.mod
└── go.sum
```

### After

```
simple-sandbox/
├── main.go              # CLI entry point: parse subcommands, dispatch
├── serve.go             # serve subcommand: HTTP server, config, routes, handlers
├── run.go               # run subcommand: one-shot execution, output formatting
├── executor.go          # Docker container lifecycle (unchanged)
├── setup.go             # Auto-setup: embed Dockerfile, check/build image
├── executor_test.go     # (unchanged)
├── serve_test.go        # HTTP handler tests (moved from main_test.go)
├── run_test.go          # run subcommand tests
├── setup_test.go        # Auto-setup tests
├── Dockerfile.sandbox   # (unchanged, also embedded into binary)
├── setup.sh             # (unchanged)
├── go.mod
└── go.sum
```

### File responsibilities

- `main.go`: Parse args, dispatch to `serve` or `run`. Print help/version. No business logic.
- `serve.go`: Everything currently in main.go except `main()` — config, router, handlers, server lifecycle. Exported as `runServe(port int, maxConcurrent int, timeout int, maxTimeout int) error`.
- `run.go`: Create executor, call Execute, print stdout/stderr, exit with code. Exported as `runCode(code string, timeout int) error`.
- `setup.go`: `//go:embed Dockerfile.sandbox`, `ensureImage(ctx)` function that checks if image exists and builds it if not.
- `executor.go`: Untouched.

## Naming Changes

| Before | After |
|--------|-------|
| Binary: `simple-sandbox` | Binary: `sidegent` |
| Docker image: `simple-sandbox-python` | Docker image: `sidegent-python` |
| Env var `SANDBOX_IMAGE` default: `simple-sandbox-python` | Default: `sidegent-python` |
| `.gitignore`: `simple-sandbox` | `.gitignore`: `sidegent` |

## CLI Framework

Use Go stdlib `flag` package with manual subcommand parsing. No external dependency (cobra is overkill for 2 subcommands). Pattern:

```go
func main() {
    if len(os.Args) < 2 {
        printUsage()
        os.Exit(1)
    }
    switch os.Args[1] {
    case "serve":
        // parse serve flags from os.Args[2:]
    case "run":
        // parse run flags from os.Args[2:]
    case "--help", "-h":
        printUsage()
    case "--version", "-v":
        printVersion()
    default:
        printUsage()
        os.Exit(1)
    }
}
```

## Version

Hardcoded as `const version = "0.1.0"` in main.go. Can be overridden at build time with `-ldflags` later when goreleaser is added.

## What Does NOT Change

- `executor.go` — container lifecycle logic is untouched
- HTTP API contract (`POST /execute`, `GET /health`) — unchanged
- Security model — unchanged
- `Dockerfile.sandbox` content — unchanged
- `setup.sh` — unchanged (still useful for EC2 with gVisor)
- All existing executor tests — still pass
