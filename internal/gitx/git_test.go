package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nox-dev/nox/internal/execx"
)

func TestCloneAtAndPublishPreserveSourceCheckout(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	run := execx.Runner{}
	git := Git{Runner: run}
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		result, err := run.Run(ctx, execx.Command{Name: "git", Args: args, Dir: dir, Env: execx.WithEnv(os.Environ(), map[string]string{
			"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
			"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
		})})
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, result.Stderr)
		}
		return strings.TrimSpace(result.Stdout)
	}
	gitRun(source, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "remove.txt"), []byte("remove\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "mode.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(source, "add", "README.md", "remove.txt", "mode.sh")
	gitRun(source, "commit", "-m", "base")
	base := gitRun(source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "dirty.txt"), []byte("do not copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := git.CloneAt(ctx, source, base, workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".hidden"), []byte("hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workspace, "remove.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(workspace, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(workspace, "link.txt")); err != nil {
		t.Fatal(err)
	}
	branch := "nox/test"
	sha, changed, err := git.Publish(ctx, source, base, branch, workspace, "nox: test changes")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || sha == "" {
		t.Fatalf("expected published changes, changed=%v sha=%q", changed, sha)
	}
	gotBranch := gitRun(source, "rev-parse", "refs/heads/"+branch)
	if gotBranch != sha {
		t.Fatalf("branch SHA=%s, publish SHA=%s", gotBranch, sha)
	}
	if parent := gitRun(source, "rev-parse", sha+"^1"); parent != base {
		t.Fatalf("publish parent=%s, base=%s", parent, base)
	}
	if current := gitRun(source, "branch", "--show-current"); current != "main" {
		t.Fatalf("source checkout moved to %q", current)
	}
	if _, err := os.Stat(filepath.Join(source, "dirty.txt")); err != nil {
		t.Fatalf("source dirty file changed: %v", err)
	}
	if tree := gitRun(source, "ls-tree", "-r", "--name-only", sha); !strings.Contains(tree, "new.txt") || !strings.Contains(tree, ".hidden") || !strings.Contains(tree, "link.txt") {
		t.Fatalf("published tree missing files: %s", tree)
	}
	if tree := gitRun(source, "ls-tree", "-r", "--name-only", sha); strings.Contains(tree, "remove.txt") {
		t.Fatal("deleted file leaked into published tree")
	}
	if mode := gitRun(source, "ls-tree", sha, "mode.sh"); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("mode.sh mode = %q, want executable", mode)
	}
	if tree := gitRun(source, "ls-tree", "-r", "--name-only", sha); strings.Contains(tree, "dirty.txt") {
		t.Fatal("dirty source file leaked into published tree")
	}
}

func TestPublishRejectsExistingBranch(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	initFixtureRepo(t, source)
	git := Git{Runner: execx.Runner{}}
	base, err := git.ResolveCommit(ctx, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := git.CloneAt(ctx, source, base, workspace); err != nil {
		t.Fatal(err)
	}
	if _, _, err := git.Publish(ctx, source, base, "main", workspace, "nox: collision"); err == nil {
		t.Fatal("expected existing branch error")
	}
}

func initFixtureRepo(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	run := execx.Runner{}
	env := execx.WithEnv(os.Environ(), map[string]string{
		"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
	})
	git := func(args ...string) {
		t.Helper()
		result, err := run.Run(ctx, execx.Command{Name: "git", Args: args, Dir: dir, Env: env})
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, result.Stderr)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "remove.txt"), []byte("remove\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "file.txt", "remove.txt", "mode.sh")
	git("commit", "-m", "base")
}
