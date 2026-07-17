package gitx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nox-dev/nox/internal/execx"
)

type Git struct {
	Runner execx.CommandRunner
}

type Identity struct {
	Name  string
	Email string
}

func (g Git) command(ctx context.Context, dir string, args ...string) (execx.Result, error) {
	return g.Runner.Run(ctx, execx.Command{Name: "git", Args: args, Dir: dir})
}

func (g Git) Identity(ctx context.Context, repo string) (Identity, error) {
	read := func(key string) (string, error) {
		result, err := g.command(ctx, repo, "config", "--get", key)
		if err != nil {
			return "", fmt.Errorf("read Git %s: %w", key, err)
		}
		value := strings.TrimSpace(result.Stdout)
		if value == "" {
			return "", fmt.Errorf("Git %s is empty", key)
		}
		return value, nil
	}
	name, err := read("user.name")
	if err != nil {
		return Identity{}, fmt.Errorf("Git author identity is unavailable: %w; configure user.name and user.email", err)
	}
	email, err := read("user.email")
	if err != nil {
		return Identity{}, fmt.Errorf("Git author identity is unavailable: %w; configure user.name and user.email", err)
	}
	return Identity{Name: name, Email: email}, nil
}

func (g Git) RepoRoot(ctx context.Context, path string) (string, error) {
	result, err := g.command(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a Git repository: %w", err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (g Git) ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("source branch is required")
	}
	result, err := g.command(ctx, repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve source %q: %w", ref, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (g Git) Dirty(ctx context.Context, repo string) (bool, error) {
	result, err := g.command(ctx, repo, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, fmt.Errorf("inspect source worktree: %w", err)
	}
	return strings.TrimSpace(result.Stdout) != "", nil
}

func (g Git) ValidateBranch(ctx context.Context, repo, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("output branch is required")
	}
	if _, err := g.command(ctx, repo, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid output branch %q: %w", branch, err)
	}
	result, err := g.command(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil && result.ExitCode == 0 {
		return fmt.Errorf("output branch already exists: %s", branch)
	}
	if result.ExitCode != 1 {
		return fmt.Errorf("check output branch %q: %w", branch, err)
	}
	return nil
}

func (g Git) CloneAt(ctx context.Context, repo, sha, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create clone parent: %w", err)
	}
	if _, err := g.Runner.Run(ctx, execx.Command{
		Name: "git",
		Args: []string{"clone", "--no-hardlinks", "--no-local", repo, destination},
	}); err != nil {
		return fmt.Errorf("clone source: %w", err)
	}
	if _, err := g.command(ctx, destination, "checkout", "--detach", sha); err != nil {
		return fmt.Errorf("checkout source SHA: %w", err)
	}
	return nil
}

func (g Git) Status(ctx context.Context, repo string) (string, error) {
	result, err := g.command(ctx, repo, "status", "--short", "--branch")
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func (g Git) Patch(ctx context.Context, repo, base string) (string, error) {
	result, err := g.command(ctx, repo, "diff", "--binary", base)
	if err != nil {
		return "", fmt.Errorf("create patch: %w", err)
	}
	return result.Stdout, nil
}

func (g Git) CommitPatch(ctx context.Context, repo, base, commit string) (string, error) {
	result, err := g.command(ctx, repo, "diff", "--binary", base, commit)
	if err != nil {
		return "", fmt.Errorf("create commit patch: %w", err)
	}
	return result.Stdout, nil
}

func (g Git) Publish(ctx context.Context, sourceRepo, baseSHA, outputBranch, workspace, message string, identity Identity) (sha string, changed bool, err error) {
	if err := g.ValidateBranch(ctx, sourceRepo, outputBranch); err != nil {
		return "", false, err
	}

	parent := filepath.Dir(workspace)
	publishDir, err := os.MkdirTemp(parent, "publish-")
	if err != nil {
		return "", false, fmt.Errorf("create publish clone: %w", err)
	}
	defer os.RemoveAll(publishDir)

	if _, err := g.Runner.Run(ctx, execx.Command{
		Name: "git",
		Args: []string{"clone", "--no-hardlinks", "--no-local", sourceRepo, publishDir},
	}); err != nil {
		return "", false, fmt.Errorf("clone publish repository: %w", err)
	}
	if _, err := g.command(ctx, publishDir, "checkout", "--detach", baseSHA); err != nil {
		return "", false, fmt.Errorf("prepare publish base: %w", err)
	}
	if _, err := g.command(ctx, publishDir, "clean", "-fdx"); err != nil {
		return "", false, fmt.Errorf("clean publish tree: %w", err)
	}
	if err := clearWorkingTree(publishDir); err != nil {
		return "", false, fmt.Errorf("clear publish tree: %w", err)
	}
	if err := copyTree(workspace, publishDir); err != nil {
		return "", false, fmt.Errorf("copy sandbox changes: %w", err)
	}

	if _, err := g.command(ctx, publishDir, "-c", "core.excludesFile=/dev/null", "add", "-A"); err != nil {
		return "", false, fmt.Errorf("stage sandbox changes: %w", err)
	}
	check, checkErr := g.command(ctx, publishDir, "diff", "--cached", "--quiet")
	if checkErr == nil && check.ExitCode == 0 {
		return "", false, nil
	}
	if check.ExitCode != 1 {
		return "", false, fmt.Errorf("inspect staged changes: %w", checkErr)
	}

	commitEnv := execx.WithEnv(os.Environ(), map[string]string{
		"GIT_AUTHOR_NAME":     identity.Name,
		"GIT_AUTHOR_EMAIL":    identity.Email,
		"GIT_COMMITTER_NAME":  identity.Name,
		"GIT_COMMITTER_EMAIL": identity.Email,
	})
	_, err = g.Runner.Run(ctx, execx.Command{
		Name: "git",
		Args: []string{"-c", "core.hooksPath=/dev/null", "commit", "-m", message},
		Dir:  publishDir,
		Env:  commitEnv,
	})
	if err != nil {
		return "", false, fmt.Errorf("create publish commit: %w", err)
	}
	shaResult, err := g.command(ctx, publishDir, "rev-parse", "HEAD")
	if err != nil {
		return "", false, fmt.Errorf("read publish SHA: %w", err)
	}
	sha = strings.TrimSpace(shaResult.Stdout)

	// Transfer the commit objects and create only the requested local ref. The
	// no-write-fetch-head flag avoids changing unrelated repository metadata.
	if _, err := g.Runner.Run(ctx, execx.Command{
		Name: "git",
		Args: []string{"fetch", "--no-write-fetch-head", publishDir, "HEAD:refs/heads/" + outputBranch},
		Dir:  sourceRepo,
	}); err != nil {
		return "", false, fmt.Errorf("publish local branch: %w", err)
	}
	return sha, true, nil
}

func clearWorkingTree(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(source, destination string) error {
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
		if containsGitPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		if err := removeTarget(target); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported special file in sandbox: %s", rel)
		}
	})
}

func containsGitPath(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	return false
}

func removeTarget(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func copyFile(source, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(destination, mode)
}
