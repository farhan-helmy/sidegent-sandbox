package main

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func TestEnsureImageAlreadyExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// python:3.12-slim should already be present (pulled by Dockerfile.sandbox base).
	err := ensureImage(ctx, "python:3.12-slim")
	if err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
}

func TestEnsureImageBuildsWhenMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	const testImage = "simple-sandbox-test-ensure:latest"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Remove the test image if it exists so we can verify it gets built.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()

	cli.ImageRemove(ctx, testImage, image.RemoveOptions{Force: true})

	// Verify the image is gone.
	_, _, err = cli.ImageInspectWithRaw(ctx, testImage)
	if err == nil {
		t.Fatal("expected image to be removed, but it still exists")
	}

	// Build via ensureImage.
	if err := ensureImage(ctx, testImage); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}

	// Verify the image now exists.
	_, _, err = cli.ImageInspectWithRaw(ctx, testImage)
	if err != nil {
		t.Fatalf("image should exist after ensureImage: %v", err)
	}

	// Cleanup: remove the test image.
	cli.ImageRemove(ctx, testImage, image.RemoveOptions{Force: true})
}
