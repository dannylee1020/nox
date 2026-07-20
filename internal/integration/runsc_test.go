//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/agent"
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

type integrationAdapter struct {
	command string
}

func (integrationAdapter) Name() string { return "integration" }
func (integrationAdapter) Environment() map[string]string {
	return map[string]string{"HOME": "/home/nox"}
}
func (a integrationAdapter) Command() []string                       { return []string{"sh", "-lc", a.command} }
func (integrationAdapter) PermissionMode() string                    { return "outer-sandbox" }
func (integrationAdapter) Prompt(context agent.PromptContext) string { return context.Task }

func TestRunscLaunch(t *testing.T) {
	if os.Getenv("NOX_RUNSC_INTEGRATION") != "1" {
		t.Skip("set NOX_RUNSC_INTEGRATION=1 on a Linux Docker or Colima host to run the real Nox launch")
	}
	source := t.TempDir()
	initIntegrationRepo(t, source)
	state := t.TempDir()
	base := gitOutput(t, source, "rev-parse", "main")
	orchestrator := noxrun.New()
	orchestrator.Adapter = integrationAdapter{command: "nox-fixture-tool > generated.txt"}
	result, err := orchestrator.Launch(context.Background(), noxrun.Config{
		Repo: source, From: "main", OutputBranch: "nox/integration",
		Task:       "create generated.txt",
		Validation: "test \"$(cat generated.txt)\" = integration-ok && test \"$(nox-fixture-tool)\" = integration-ok", Network: "none",
		Image: runnerImage(), StateRoot: state, Timeout: 5 * time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		var logs strings.Builder
		for _, name := range []string{"setup.log", "agent.log", "validation.log"} {
			if data, readErr := os.ReadFile(filepath.Join(state, result.Metadata.RunID, name)); readErr == nil {
				fmt.Fprintf(&logs, "\n==> %s <==\n%s", name, data)
			}
		}
		t.Fatalf("%v%s", err, logs.String())
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
	if setupLog, err := os.ReadFile(filepath.Join(state, result.Metadata.RunID, "setup.log")); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(setupLog), "fixture tool installed") {
		t.Fatalf("setup log = %q", setupLog)
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

func TestRepositoryGoSetup(t *testing.T) {
	if os.Getenv("NOX_GO_SETUP_INTEGRATION") != "1" {
		t.Skip("set NOX_GO_SETUP_INTEGRATION=1 to verify this repository's networked Go setup")
	}
	source := t.TempDir()
	gitRun(t, source, "init", "-b", "main")
	gitRun(t, source, "config", "user.name", "Integration User")
	gitRun(t, source, "config", "user.email", "integration@example.com")
	setup, err := os.ReadFile(filepath.Join("..", "..", ".nox", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":          "module example.com/nox-setup-fixture\n\ngo 1.26\n",
		"fixture.go":      "package fixture\n\nfunc Value() int { return 1 }\n",
		"fixture_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal(\"unexpected value\") } }\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setupDir := filepath.Join(source, ".nox")
	if err := os.MkdirAll(setupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setupDir, "setup.sh"), setup, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "add", ".")
	gitRun(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")

	state := t.TempDir()
	orchestrator := noxrun.New()
	orchestrator.Adapter = integrationAdapter{command: "GOTOOLCHAIN=local go version > toolchain.txt"}
	result, err := orchestrator.Launch(context.Background(), noxrun.Config{
		Repo: source, From: "main", OutputBranch: "nox/go-setup",
		Task:       "record the configured Go toolchain",
		Validation: "GOTOOLCHAIN=local go test -race ./... && grep '^go version go1.26.0 ' toolchain.txt",
		Network:    "online", Image: runnerImage(), StateRoot: state, Timeout: 10 * time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		var logs strings.Builder
		for _, name := range []string{"setup.log", "agent.log", "validation.log"} {
			if data, readErr := os.ReadFile(filepath.Join(state, result.Metadata.RunID, name)); readErr == nil {
				fmt.Fprintf(&logs, "\n==> %s <==\n%s", name, data)
			}
		}
		t.Fatalf("%v%s", err, logs.String())
	}
	if result.Metadata.State != store.StateCompleted || result.NoChanges {
		t.Fatalf("result = %#v", result)
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
	setupDir := filepath.Join(dir, ".nox")
	if err := os.MkdirAll(setupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setup := "#!/bin/sh\nset -eu\nprintf '#!/bin/sh\\necho integration-ok\\n' > /usr/local/bin/nox-fixture-tool\nchmod 755 /usr/local/bin/nox-fixture-tool\nprintf 'fixture tool installed\\n'\n"
	if err := os.WriteFile(filepath.Join(setupDir, "setup.sh"), []byte(setup), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md", ".nox/setup.sh")
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
