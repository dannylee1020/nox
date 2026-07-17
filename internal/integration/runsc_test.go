//go:build integration

package integration

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	noxrun "github.com/nox-dev/nox/internal/run"
	"github.com/nox-dev/nox/internal/store"
)

func TestRunscRunnerImage(t *testing.T) {
	if os.Getenv("NOX_RUNSC_INTEGRATION") != "1" {
		t.Skip("set NOX_RUNSC_INTEGRATION=1 on a Linux Docker or Colima host to run the real runsc check")
	}
	image := runnerImage()
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

func TestRunscGenericLaunch(t *testing.T) {
	if os.Getenv("NOX_RUNSC_INTEGRATION") != "1" {
		t.Skip("set NOX_RUNSC_INTEGRATION=1 on a Linux Docker or Colima host to run the real Nox launch")
	}
	source := t.TempDir()
	initIntegrationRepo(t, source)
	state := t.TempDir()
	base := gitOutput(t, source, "rev-parse", "main")
	orchestrator := noxrun.New()
	result, err := orchestrator.Launch(context.Background(), noxrun.Config{
		Repo: source, From: "main", OutputBranch: "nox/integration", Agent: "generic",
		AgentCommand: "printf 'integration-ok\\n' > generated.txt", Task: "create generated.txt",
		Validation: "test \"$(cat generated.txt)\" = integration-ok", Network: "none",
		Image: runnerImage(), StateRoot: state, Timeout: 5 * time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatal("expected a result branch")
	}
	if result.Metadata.State != store.StateCompleted {
		t.Fatalf("state = %q", result.Metadata.State)
	}
	if result.Metadata.WorkspaceVolume == "" {
		t.Fatal("workspace volume was not recorded")
	}
	if !result.Metadata.WorkspaceExported {
		t.Fatal("workspace export was not recorded")
	}
	if _, err := os.Stat(result.Metadata.Workspace); !os.IsNotExist(err) {
		t.Fatalf("successful workspace was retained: %v", err)
	}
	resultSHA := gitOutput(t, source, "rev-parse", "refs/heads/nox/integration")
	if parent := gitOutput(t, source, "rev-parse", resultSHA+"^1"); parent != base {
		t.Fatalf("result parent = %s, want %s", parent, base)
	}
	if author := gitOutput(t, source, "show", "-s", "--format=%an <%ae>|%cn <%ce>", resultSHA); author != "Integration User <integration@example.com>|Integration User <integration@example.com>" {
		t.Fatalf("result identity = %q", author)
	}
	if got := gitOutput(t, source, "show", "nox/integration:generated.txt"); got != "integration-ok" {
		t.Fatalf("generated.txt = %q", got)
	}
	if current := gitOutput(t, source, "branch", "--show-current"); current != "main" {
		t.Fatalf("source checkout changed to %q", current)
	}
	volumes := exec.CommandContext(context.Background(), "docker", "volume", "ls", "-q", "--filter", "label=io.nox.run-id="+result.Metadata.RunID)
	if output, err := volumes.CombinedOutput(); err != nil {
		t.Fatalf("list run volumes: %v\n%s", err, output)
	} else if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("workspace volume was retained: %s", output)
	}
}

func runnerImage() string {
	if image := os.Getenv("NOX_RUNNER_IMAGE"); image != "" {
		return image
	}
	return "nox-runner:v0"
}

func initIntegrationRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.name", "Integration User")
	gitRun(t, dir, "config", "user.email", "integration@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}
