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
