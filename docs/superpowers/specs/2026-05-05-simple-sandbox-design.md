# Simple Sandbox — Minimal AI Code Interpreter

## Overview

A single Go binary that accepts Python code via HTTP, runs it in a gVisor-isolated Docker container, and returns stdout/stderr. Deployable on one EC2 instance.

**Use case**: LLM agents execute Python code in a secure sandbox and get results back.

## Constraints

- Stateless: each request is independent, no session persistence
- Air-gapped: no internet access inside sandbox
- Python only: Python 3.12+ with pre-baked common packages
- Single machine: one EC2 instance, no distributed coordination

## Architecture

```
Client (LLM agent)
    │
    │  POST /execute  { "code": "print(1+1)", "timeout": 30 }
    ▼
┌─────────────────────┐
│  Go HTTP Server      │  :8080
│  (net/http stdlib)   │
│                      │
│  ┌─────────────────┐ │
│  │ Docker SDK       │ │  Creates container per request
│  │ (gVisor runtime) │ │  Waits for exit, collects output
│  └─────────────────┘ │
└─────────────────────┘
    │
    ▼
┌─────────────────────┐
│  Docker + gVisor     │  runsc runtime
│  ┌─────────────────┐ │
│  │ python:3.12-slim │ │  Air-gapped, no network
│  │ + common packages│ │  Memory/CPU limited
│  │ (numpy, pandas…) │ │  Timeout enforced
│  └─────────────────┘ │
└─────────────────────┘
```

## API

### `POST /execute`

Run Python code in a sandbox.

**Request:**
```json
{
  "code": "import math\nprint(math.sqrt(144))",
  "timeout": 30
}
```

- `code` (string, required): Python code to execute
- `timeout` (int, optional): Max execution time in seconds. Default 30, max 120.

**Response 200:**
```json
{
  "stdout": "12.0\n",
  "stderr": "",
  "exit_code": 0,
  "duration_ms": 245
}
```

**Response 408 (timeout):**
```json
{
  "error": "execution timed out after 30s"
}
```

**Response 400 (bad request):**
```json
{
  "error": "code is required"
}
```

**Response 503 (overloaded):**
```json
{
  "error": "too many concurrent executions"
}
```

### `GET /health`

Returns `200 OK` with `{"status": "ok"}`. Used for load balancer health checks.

## Container Lifecycle (per request)

1. Acquire semaphore slot (bounded concurrency)
2. Write code string to a temp file on host
3. Create container via Docker SDK:
   - Image: `simple-sandbox-python` (pre-built)
   - Runtime: `runsc` (gVisor)
   - Network: `none`
   - Memory: 512MB
   - CPU: 1 core
   - PID limit: 64
   - Read-only rootfs + tmpfs at `/tmp` (64MB)
   - No capabilities (`--cap-drop=ALL`)
   - No new privileges
4. Copy temp file into container at `/tmp/code.py`
5. Start container (entrypoint: `python /tmp/code.py`)
6. Wait for exit with timeout context (context.WithTimeout)
7. If timeout: kill and remove container, return 408
8. If completed: read stdout/stderr via Docker logs API
9. Remove container (force)
10. Clean up temp file
11. Release semaphore slot
12. Return response

## Security

| Measure | Purpose |
|---------|---------|
| gVisor runtime (`runsc`) | Syscall interception — sandboxed code never touches host kernel directly |
| `--network=none` | Air-gapped, no outbound connections |
| `--memory=512m` | Prevent memory exhaustion |
| `--cpus=1` | Prevent CPU starvation |
| `--pids-limit=64` | Prevent fork bombs |
| `--read-only` rootfs | Prevent filesystem tampering |
| `--cap-drop=ALL` | No Linux capabilities |
| `--security-opt=no-new-privileges` | Prevent privilege escalation |
| tmpfs `/tmp` (64MB) | Writable scratch space, size-limited |
| Timeout enforcement | Kill containers that run too long |
| Concurrency semaphore | Prevent host resource exhaustion |

## Concurrency

A bounded semaphore limits concurrent executions. Default: 10 concurrent containers on a `t3.medium` (2 vCPU, 4GB RAM). Requests beyond the limit get 503.

## Python Sandbox Image

`Dockerfile.sandbox` builds the pre-baked Python image:

```dockerfile
FROM python:3.12-slim
RUN pip install --no-cache-dir numpy pandas matplotlib requests scipy sympy
# No CMD — entrypoint provided at container creation
```

Pre-installed packages cover common AI code interpreter needs. The image is built once and reused for all executions.

## Project Structure

```
simple-sandbox/
├── main.go              # HTTP server, config, /execute + /health endpoints
├── executor.go          # Docker SDK container lifecycle
├── Dockerfile           # API server container (optional, for containerized deployment)
├── Dockerfile.sandbox   # Python sandbox image with pre-baked packages
├── go.mod
├── go.sum
├── setup.sh             # EC2 instance setup (Docker + gVisor)
└── docs/
    └── superpowers/
        └── specs/
            └── 2026-05-05-simple-sandbox-design.md
```

## Configuration

All via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP server port |
| `MAX_CONCURRENT` | `10` | Max concurrent sandbox executions |
| `DEFAULT_TIMEOUT` | `30` | Default execution timeout (seconds) |
| `MAX_TIMEOUT` | `120` | Maximum allowed timeout (seconds) |
| `SANDBOX_IMAGE` | `simple-sandbox-python` | Docker image for sandbox containers |
| `SANDBOX_MEMORY` | `512m` | Memory limit per container |
| `SANDBOX_CPUS` | `1` | CPU limit per container |

## Deployment

**Target**: Single EC2 `t3.medium` (2 vCPU, 4GB RAM, ~$30/mo)

**`setup.sh` does:**
1. Install Docker
2. Install gVisor (`runsc`) runtime
3. Configure Docker to use `runsc` as available runtime
4. Build the sandbox Python image
5. Build and start the Go binary (or run via systemd)

**To run locally (dev):**
```bash
# Install gVisor: https://gvisor.dev/docs/user_guide/install/
# Then:
docker build -t simple-sandbox-python -f Dockerfile.sandbox .
go run .
```

## What This Does NOT Include

- Authentication (add an API key middleware if needed later)
- Persistent sessions or state
- Multiple language runtimes
- Internet access inside sandbox
- Pause/resume/snapshots
- Distributed orchestration
- Analytics or observability beyond stdout logs
- File upload/download to sandbox

These can all be added incrementally if needed.
