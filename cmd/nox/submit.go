package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/gitx"
	"github.com/nox-dev/nox/internal/remote"
)

func submit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", ".", "local Git repository")
	from := fs.String("from", "", "GitHub base branch to submit")
	title := fs.String("title", "", "pull request title")
	task := fs.String("task", "", "agent task")
	taskFile := fs.String("task-file", "", "file containing the agent task")
	validate := fs.String("validate", "", "explicit validation command")
	network := fs.String("network", "online", "network mode: online or none")
	timeout := fs.Duration("timeout", 2*time.Hour, "maximum remote run duration")
	pollInterval := fs.Duration("poll-interval", 2*time.Second, "remote status polling interval")
	detach := fs.Bool("detach", false, "return after the remote run is accepted")
	jsonOutput := fs.Bool("json", false, "emit one JSON result")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *task != "" && *taskFile != "" {
		return errors.New("use only one of --task and --task-file")
	}
	if *task == "" && *taskFile == "" {
		return errors.New("one of --task or --task-file is required")
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("--title is required")
	}
	if strings.TrimSpace(*validate) == "" {
		return errors.New("--validate is required")
	}
	if *timeout <= 0 || *timeout > 24*time.Hour {
		return errors.New("--timeout must be between 1s and 24h")
	}
	if *pollInterval <= 0 {
		return errors.New("--poll-interval must be positive")
	}
	if *network != "online" && *network != "none" {
		return errors.New("--network must be online or none")
	}
	if *taskFile != "" {
		data, err := os.ReadFile(*taskFile)
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		*task = string(data)
	}

	remoteURL := os.Getenv("NOX_REMOTE_URL")
	apiToken := os.Getenv("NOX_API_TOKEN")
	client, err := remote.NewClient(remoteURL, apiToken, nil)
	if err != nil {
		return err
	}

	git := gitx.Git{Runner: execx.Runner{}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repoRoot, err := git.RepoRoot(ctx, *repo)
	if err != nil {
		return err
	}
	baseBranch := strings.TrimSpace(*from)
	if baseBranch == "" {
		baseBranch, err = git.CurrentBranch(ctx, repoRoot)
		if err != nil {
			return err
		}
	}
	if dirty, dirtyErr := git.Dirty(ctx, repoRoot); dirtyErr != nil {
		return dirtyErr
	} else if dirty && !*jsonOutput {
		fmt.Fprintln(os.Stderr, "warning: local uncommitted changes are ignored; remote execution uses GitHub state")
	}
	originURL, err := git.RemoteURL(ctx, repoRoot, "origin")
	if err != nil {
		return err
	}
	repository, err := remote.ParseGitHubRemoteURL(originURL)
	if err != nil {
		return err
	}
	baseCommit, err := git.RemoteBranchCommit(ctx, repoRoot, "origin", baseBranch)
	if err != nil {
		return fmt.Errorf("resolve GitHub base branch: %w", err)
	}

	seconds := int((*timeout + time.Second - 1) / time.Second)
	status, err := client.Submit(ctx, remote.RunRequest{
		Repository:     repository,
		BaseBranch:     baseBranch,
		BaseCommit:     baseCommit,
		Title:          strings.TrimSpace(*title),
		Task:           *task,
		Validation:     *validate,
		Network:        *network,
		TimeoutSeconds: seconds,
	})
	if err != nil {
		return err
	}
	if *detach {
		if *jsonOutput {
			return printSubmitJSON(status)
		}
		fmt.Fprintf(os.Stdout, "submitted remote run: %s\n", status.RunID)
		return nil
	}
	if !*jsonOutput {
		fmt.Fprintf(os.Stderr, "submitted remote run: %s\n", status.RunID)
	}
	lastState := ""
	final, waitErr := client.Wait(ctx, status, *pollInterval, func(current remote.RunStatus) {
		if *jsonOutput || current.State == lastState {
			return
		}
		lastState = current.State
		fmt.Fprintf(os.Stderr, "remote run %s: %s\n", current.RunID, current.State)
	})
	if waitErr != nil {
		if errors.Is(waitErr, context.Canceled) {
			cancelContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = client.Cancel(cancelContext, status.RunID)
			return fmt.Errorf("remote run %s cancellation requested", status.RunID)
		}
		return waitErr
	}
	if *jsonOutput {
		if err := printSubmitJSON(final); err != nil {
			return err
		}
		return submitStatusError(final)
	}
	return printSubmitResult(final)
}

func printSubmitResult(status remote.RunStatus) error {
	switch status.State {
	case remote.StateCompleted:
		fmt.Printf("completed: remote run %s\n", status.RunID)
		if status.PullRequestURL == "" {
			return errors.New("remote run completed without a pull request URL")
		}
		fmt.Printf("pull request: %s\n", status.PullRequestURL)
		return nil
	case remote.StateNoChanges:
		fmt.Printf("completed: remote run %s made no changes\n", status.RunID)
		return nil
	default:
		return submitStatusError(status)
	}
}

func submitStatusError(status remote.RunStatus) error {
	switch status.State {
	case remote.StateCompleted:
		if status.PullRequestURL == "" {
			return errors.New("remote run completed without a pull request URL")
		}
		return nil
	case remote.StateNoChanges:
		return nil
	case remote.StateCancelled:
		return fmt.Errorf("remote run %s was cancelled", status.RunID)
	case remote.StateFailed:
		if status.Error != "" {
			return fmt.Errorf("remote run %s failed: %s", status.RunID, status.Error)
		}
		return fmt.Errorf("remote run %s failed", status.RunID)
	default:
		return fmt.Errorf("remote run %s ended in unexpected state %q", status.RunID, status.State)
	}
}

func printSubmitJSON(status remote.RunStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode remote result: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
