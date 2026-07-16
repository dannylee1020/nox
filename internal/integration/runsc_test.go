//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunscRunnerImage(t *testing.T) {
	if os.Getenv("NOX_RUNSC_INTEGRATION") != "1" {
		t.Skip("set NOX_RUNSC_INTEGRATION=1 on a Linux Docker host to run the real runsc check")
	}
	image := os.Getenv("NOX_RUNNER_IMAGE")
	if image == "" {
		image = "nox-runner:v0"
	}
	ctx := context.Background()
	info := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .Runtimes}}")
	output, err := info.CombinedOutput()
	if err != nil {
		t.Fatalf("docker info: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "runsc") {
		t.Fatalf("runsc is not registered: %s", output)
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--runtime", "runsc", "--network", "none", image, "true")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runsc smoke container: %v\n%s", err, output)
	}
}
