package run

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nox-dev/nox/internal/agent"
	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/sandbox"
	"github.com/nox-dev/nox/internal/store"
)

type fakeDockerRunner struct {
	volumePath string
	removed    bool
	agentDone  bool
	setupRuns  int
	setupCode  int
}

type testAdapter struct {
	command string
}

func (testAdapter) Name() string { return "test" }
func (testAdapter) Environment() map[string]string {
	return map[string]string{"HOME": "/home/nox"}
}
func (a testAdapter) Command() []string                       { return []string{"sh", "-lc", a.command} }
func (testAdapter) PermissionMode() string                    { return "outer-sandbox" }
func (testAdapter) Prompt(context agent.PromptContext) string { return context.Task }

func newTestOrchestrator(command string) Orchestrator {
	orchestrator := New()
	orchestrator.Adapter = testAdapter{command: command}
	return orchestrator
}

func (f *fakeDockerRunner) Run(_ context.Context, command execx.Command) (execx.Result, error) {
	if len(command.Args) == 0 {
		return execx.Result{ExitCode: 1}, nil
	}
	switch command.Args[0] {
	case "volume":
		if len(command.Args) > 1 && command.Args[1] == "create" {
			var err error
			f.volumePath, err = os.MkdirTemp("", "nox-fake-volume-")
			if err != nil {
				return execx.Result{ExitCode: 1}, err
			}
			return execx.Result{Stdout: "fake-volume\n", ExitCode: 0}, nil
		}
		if len(command.Args) > 1 && command.Args[1] == "rm" {
			_ = os.RemoveAll(f.volumePath)
			return execx.Result{ExitCode: 0}, nil
		}
	case "create":
		return execx.Result{Stdout: "fake-container\n", ExitCode: 0}, nil
	case "cp":
		if len(command.Args) != 3 {
			return execx.Result{ExitCode: 1}, nil
		}
		source, destination := command.Args[1], command.Args[2]
		if strings.HasPrefix(source, "fake-container:/workspace/") {
			return execx.Result{ExitCode: 0}, copyWorkspace(f.volumePath, destination)
		}
		if strings.HasPrefix(destination, "fake-container:/workspace") {
			source = strings.TrimSuffix(source, string(filepath.Separator)+".")
			return execx.Result{ExitCode: 0}, copyWorkspace(source, f.volumePath)
		}
	case "rm":
		f.removed = true
	}
	return execx.Result{ExitCode: 0}, nil
}

func (f *fakeDockerRunner) Stream(_ context.Context, command execx.Command, stdout, _ io.Writer) (execx.Result, error) {
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "git config --global") {
		return execx.Result{ExitCode: 0}, nil
	}
	if strings.Contains(joined, "test -f /workspace/.nox/setup.sh") {
		if _, err := os.Stat(filepath.Join(f.volumePath, ".nox", "setup.sh")); err != nil {
			return execx.Result{ExitCode: 1}, nil
		}
		return execx.Result{ExitCode: 0}, nil
	}
	if strings.Contains(joined, "sh /workspace/.nox/setup.sh") {
		f.setupRuns++
		_, _ = io.WriteString(stdout, "setup ok\n")
		return execx.Result{ExitCode: f.setupCode}, nil
	}
	if strings.Contains(joined, " false") {
		return execx.Result{ExitCode: 1}, nil
	}
	if !f.agentDone {
		f.agentDone = true
		if strings.Contains(joined, "no-op") {
			return execx.Result{ExitCode: 0}, nil
		}
		if err := os.WriteFile(filepath.Join(f.volumePath, "generated.txt"), []byte("generated\n"), 0o644); err != nil {
			return execx.Result{ExitCode: 1}, err
		}
	} else if strings.Contains(joined, "test -f generated.txt") {
		if _, err := os.Stat(filepath.Join(f.volumePath, "generated.txt")); err != nil {
			return execx.Result{ExitCode: 1}, nil
		}
	} else if strings.Contains(joined, "sh -lc true") {
		// No-op validation.
	} else if strings.Contains(joined, "sh -lc") {
		if err := os.WriteFile(filepath.Join(f.volumePath, "generated.txt"), []byte("generated\n"), 0o644); err != nil {
			return execx.Result{ExitCode: 1}, err
		}
	}
	_, _ = io.WriteString(stdout, "ok\n")
	return execx.Result{ExitCode: 0}, nil
}

func copyWorkspace(source, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func TestLaunchPublishesValidatedLocalBranchAndTearsDown(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := newTestOrchestrator("true")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/result",
		Task: "create generated file", Validation: "test -f generated.txt",
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
	if result.Metadata.WorkspaceVolume == "" {
		t.Fatal("workspace volume was not recorded")
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

func TestLaunchRunsRepositorySetupBeforeAgent(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	addSetupFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := newTestOrchestrator("no-op")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		RunID: "setup-success", Repo: source, From: "main", OutputBranch: "nox/setup-success",
		Task: "verify setup", Validation: "true",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.setupRuns != 1 || !fake.agentDone {
		t.Fatalf("setup runs = %d, agent done = %t", fake.setupRuns, fake.agentDone)
	}
	if !result.NoChanges {
		t.Fatal("expected no changes")
	}
	data, err := os.ReadFile(filepath.Join(state, "setup-success", "setup.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "setup ok") {
		t.Fatalf("setup log = %q", data)
	}
}

func TestLaunchStopsWhenRepositorySetupFails(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	addSetupFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{setupCode: 23}
	orchestrator := newTestOrchestrator("no-op")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		RunID: "setup-failure", Repo: source, From: "main", OutputBranch: "nox/setup-failure",
		Task: "verify setup failure", Validation: "true",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "repository setup exited with code 23") {
		t.Fatalf("setup error = %v", err)
	}
	if fake.setupRuns != 1 || fake.agentDone {
		t.Fatalf("setup runs = %d, agent done = %t", fake.setupRuns, fake.agentDone)
	}
	if result.Metadata.State != store.StateFailed || !result.Metadata.Retained {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	if branch := assertGit(t, source, "show-ref", "--verify", "--quiet", "refs/heads/nox/setup-failure"); branch != "" {
		t.Fatalf("unexpected branch output: %q", branch)
	}
}

func TestLaunchAnnouncesReadableRun(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	var announced store.Metadata
	orchestrator := newTestOrchestrator("no-op")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/announced",
		Task: "announce", Validation: "true",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
		OnStart: func(metadata store.Metadata) error {
			announced = metadata
			got, readErr := store.New(state).ReadMetadata(metadata.RunID)
			if readErr != nil {
				return readErr
			}
			if got.State != store.StateInitializing {
				t.Fatalf("announced state = %q", got.State)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if announced.RunID == "" || result.Metadata.RunID != announced.RunID {
		t.Fatalf("announced run = %#v, result = %#v", announced, result.Metadata)
	}
}

func TestLaunchUsesCallerProvidedRunID(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := newTestOrchestrator("no-op")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		RunID: "remote-run-123", Repo: source, From: "main", OutputBranch: "nox/remote-run-123",
		Task: "do nothing", Validation: "true",
		Network: "none", Image: "nox-runner:v0", StateRoot: state, Timeout: time.Minute,
		Output: io.Discard, ErrorOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.RunID != "remote-run-123" {
		t.Fatalf("run ID = %q", result.Metadata.RunID)
	}
	if _, err := store.New(state).ReadMetadata("remote-run-123"); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchNoChangesDoesNotPublish(t *testing.T) {
	source := t.TempDir()
	initRunFixture(t, source)
	state := t.TempDir()
	fake := &fakeDockerRunner{}
	orchestrator := newTestOrchestrator("no-op")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	result, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/no-change",
		Task: "do nothing", Validation: "true",
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
	orchestrator := newTestOrchestrator("true")
	orchestrator.Docker = sandbox.Docker{Runner: fake}
	_, err := orchestrator.Launch(context.Background(), Config{
		Repo: source, From: "main", OutputBranch: "nox/failure",
		Task: "create generated file", Validation: "false",
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
	assertGit(t, dir, "config", "user.name", "Fixture User")
	assertGit(t, dir, "config", "user.email", "fixture@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertGit(t, dir, "add", "README.md")
	assertGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
}

func addSetupFixture(t *testing.T, dir string) {
	t.Helper()
	setupDir := filepath.Join(dir, ".nox")
	if err := os.MkdirAll(setupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setupDir, "setup.sh"), []byte("#!/bin/sh\nset -eu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertGit(t, dir, "add", ".nox/setup.sh")
	assertGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "add setup")
}

func assertGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	result, err := (execx.Runner{}).Run(context.Background(), execx.Command{Name: "git", Args: args, Dir: dir})
	if err != nil && result.ExitCode != 1 {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}
