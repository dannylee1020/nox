package run

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/sandbox"
	"github.com/nox-dev/nox/internal/store"
)

type fakeDockerRunner struct {
	workspace string
	removed   bool
	agentDone bool
}

func (f *fakeDockerRunner) Run(_ context.Context, command execx.Command) (execx.Result, error) {
	if len(command.Args) == 0 {
		return execx.Result{ExitCode: 1}, nil
	}
	switch command.Args[0] {
	case "create":
		for _, arg := range command.Args {
			if strings.HasPrefix(arg, "type=bind,src=") && strings.HasSuffix(arg, ",dst=/workspace") {
				f.workspace = strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/workspace")
			}
		}
		return execx.Result{Stdout: "fake-container\n", ExitCode: 0}, nil
	case "rm":
		f.removed = true
	}
	return execx.Result{ExitCode: 0}, nil
}

func (f *fakeDockerRunner) Stream(_ context.Context, command execx.Command, stdout, _ io.Writer) (execx.Result, error) {
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, " false") {
		return execx.Result{ExitCode: 1}, nil
	}
	if !f.agentDone {
		f.agentDone = true
		if strings.Contains(joined, "no-op") {
			return execx.Result{ExitCode: 0}, nil
		}
		if err := os.WriteFile(filepath.Join(f.workspace, "generated.txt"), []byte("generated\n"), 0o644); err != nil {
			return execx.Result{ExitCode: 1}, err
		}
	} else if strings.Contains(joined, "test -f generated.txt") {
		if _, err := os.Stat(filepath.Join(f.workspace, "generated.txt")); err != nil {
			return execx.Result{ExitCode: 1}, nil
		}
	} else if strings.Contains(joined, "sh -lc true") {
		// No-op validation.
	} else if strings.Contains(joined, "sh -lc") {
		if err := os.WriteFile(filepath.Join(f.workspace, "generated.txt"), []byte("generated\n"), 0o644); err != nil {
			return execx.Result{ExitCode: 1}, err
		}
	}
	_, _ = io.WriteString(stdout, "ok\n")
	return execx.Result{ExitCode: 0}, nil
}

func TestLaunchPublishesValidatedLocalBranchAndTearsDown(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := New()
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/result", Agent: "generic",
		AgentCommand: "true", Task: "create generated file", Validation: "test -f generated.txt",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NoChanges {
		t.Fatal("expected a result branch")
	}
	if !fake.removed {
		t.Fatal("container was not removed")
	}
	if result.Metadata.State != store.StateCompleted {
		t.Fatalf("state = %q", result.Metadata.State)
	}
	if _, err := os.Stat(filepath.Join(result.Metadata.Workspace, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("successful workspace was retained: %v", err)
	}
	assertGit(t, source, "rev-parse", "--verify", "refs/heads/nox/result")
	current := assertGit(t, source, "branch", "--show-current")
	if current != "main" {
		t.Fatalf("current branch changed to %q", current)
	}
}

func TestLaunchNoChangesDoesNotPublish(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := New()
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/no-change", Agent: "generic",
		AgentCommand: "no-op", Task: "do nothing", Validation: "true",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoChanges {
		t.Fatal("expected no-change result")
	}
	if !fake.removed {
		t.Fatal("container was not removed")
	}
	if branch := assertGit(t, source, "show-ref", "--verify", "--quiet", "refs/heads/nox/no-change"); branch != "" {
		t.Fatalf("unexpected branch output: %q", branch)
	}
}

func TestLaunchValidationFailureDoesNotPublish(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := New()
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	_, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/failure", Agent: "generic",
		AgentCommand: "true", Task: "create generated file", Validation: "false",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if !fake.removed {
		t.Fatal("container was not removed after validation failure")
	}
	if result := assertGit(t, source, "show-ref", "--verify", "--quiet", "refs/heads/nox/failure"); result != "" {
		t.Fatalf("unexpected branch output: %q", result)
	}
}

func initRunFixture(t *testing.T, dir string) {
	t.Helper()
	assertGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertGit(t, dir, "add", "README.md")
	assertGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
}

func assertGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	result, err := (execx.Runner{}).Run(context.Background(), execx.Command{Name: "git", Args: args, Dir: dir})
	if err != nil && result.ExitCode != 1 {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}
