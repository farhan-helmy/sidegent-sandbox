package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

//go:embed Dockerfile.sandbox
var dockerfileContent []byte

func ensureImage(ctx context.Context, imageName string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	// Check if the image already exists.
	_, _, err = cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return nil
	}

	fmt.Fprintln(os.Stderr, "Building sandbox image (first run only)...")

	// Build a tar archive containing the Dockerfile.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	header := &tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfileContent)),
		Mode: 0644,
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("tar write header: %w", err)
	}
	if _, err := tw.Write(dockerfileContent); err != nil {
		return fmt.Errorf("tar write body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("tar close: %w", err)
	}

	// Build the image.
	resp, err := cli.ImageBuild(ctx, &buf, types.ImageBuildOptions{
		Tags:       []string{imageName},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	// Drain the build output so the build completes.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("reading build output: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Sandbox image ready.")
	return nil
}
