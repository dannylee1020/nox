package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/remote"
	"github.com/nox-dev/nox/internal/run"
	"github.com/nox-dev/nox/internal/sandbox"
	"github.com/nox-dev/nox/internal/store"
)

const usageText = `Nox runs coding agents inside local or remote Docker/gVisor sandboxes.

Commands:
  nox ui
  nox cancel --remote <run-id>
  nox doctor
  nox serve
  nox launch --mode feat --repo . --from main --output-branch nox/change --task "..." --validate "..."
  nox launch --mode test --repo . --from main --task-file contract.md --validate "..."
  nox submit --mode feat --repo . --from main --title "..." --task-file contract.md --validate "..."
  nox submit --mode test --repo . --from main --task-file contract.md --validate "..."
  nox inspect [--remote] <run-id>
  nox diff <run-id>
  nox cleanup <run-id>
  nox cleanup --stale
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ui":
		err = uiCommand(os.Args[2:])
	case "cancel":
		err = cancelRun(os.Args[2:])
	case "doctor":
		err = doctor(os.Args[2:])
	case "launch":
		err = launch(os.Args[2:])
	case "submit":
		err = submit(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	case "inspect":
		err = inspect(os.Args[2:])
	case "diff":
		err = diff(os.Args[2:])
	case "cleanup":
		err = cleanup(os.Args[2:])
	case "help", "--help", "-h":
		fmt.Print(usageText)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nox:", err)
		os.Exit(1)
	}
}

func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	image := fs.String("image", "nox-runner:v0", "runner image")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := (sandbox.Docker{Runner: execx.Runner{}}).Doctor(ctx, *image); err != nil {
		return err
	}
	fmt.Printf("ok: Docker can run %s with runsc\n", *image)
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", getenv("NOX_LISTEN_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	stateRoot := fs.String("state-root", getenv("NOX_STATE_ROOT", ""), "remote state directory")
	githubAPI := fs.String("github-api-url", getenv("NOX_GITHUB_API_URL", "https://api.github.com"), "GitHub API URL")
	codexHome := fs.String("codex-home", getenv("CODEX_HOME", ""), "host Codex directory")
	gitName := fs.String("git-name", getenv("NOX_GIT_NAME", "Nox Worker"), "Git author name")
	gitEmail := fs.String("git-email", getenv("NOX_GIT_EMAIL", "nox@localhost"), "Git author email")
	maxConcurrentRunsValue := fs.String("max-concurrent-runs", os.Getenv("NOX_MAX_CONCURRENT_RUNS"), "maximum active remote runs (default 5)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	maxConcurrentRuns, err := parseMaxConcurrentRuns(*maxConcurrentRunsValue)
	if err != nil {
		return err
	}
	apiToken := os.Getenv("NOX_API_TOKEN")
	githubToken := os.Getenv("NOX_GITHUB_TOKEN")
	if apiToken == "" {
		return errors.New("NOX_API_TOKEN is required")
	}
	if githubToken == "" {
		return errors.New("NOX_GITHUB_TOKEN is required")
	}
	if *stateRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*stateRoot = filepath.Join(home, ".nox", "remote")
	}
	coordinator := remote.NewCoordinator(remote.CoordinatorConfig{
		StateRoot: *stateRoot, GitHubToken: githubToken, GitHubAPIURL: *githubAPI,
		CodexHome: *codexHome, GitName: *gitName, GitEmail: *gitEmail,
	})
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	service, err := remote.NewServer(remote.ServerConfig{
		APIToken: apiToken, Executor: coordinator, MaxConcurrentRuns: maxConcurrentRuns,
		StatusRoot: filepath.Join(*stateRoot, "jobs"),
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
	}()
	fmt.Printf("nox serve listening on %s\n", listener.Addr().String())
	if err := httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func parseMaxConcurrentRuns(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("--max-concurrent-runs/NOX_MAX_CONCURRENT_RUNS must be a positive integer, got %q", value)
	}
	return limit, nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func launch(args []string) error {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", ".", "local Git repository")
	from := fs.String("from", "", "local branch or ref to launch from")
	mode := fs.String("mode", "feat", "run mode: feat or test")
	outputBranch := fs.String("output-branch", "", "new local branch to create after validation (feat mode only)")
	task := fs.String("task", "", "agent task")
	taskFile := fs.String("task-file", "", "file containing the agent task")
	validate := fs.String("validate", "", "explicit validation command")
	network := fs.String("network", "online", "network mode: online or none")
	image := fs.String("image", "nox-runner:v0", "runner image")
	stateRoot := fs.String("state-root", "", "run state directory")
	codexHome := fs.String("codex-home", "", "host Codex directory to mount read-only")
	cpu := fs.String("cpu", "2", "container CPU limit")
	memory := fs.String("memory", "4g", "container memory limit")
	pids := fs.Int("pids-limit", 256, "container PID limit")
	timeout := fs.Duration("timeout", 2*time.Hour, "maximum run duration")
	jsonOutput := fs.Bool("json", false, "emit one JSON result")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	intent, err := run.ParseIntent(*mode)
	if err != nil {
		return err
	}
	if intent == run.IntentTest && *outputBranch != "" {
		return errors.New("--output-branch cannot be used in test mode")
	}
	if *task != "" && *taskFile != "" {
		return errors.New("use only one of --task and --task-file")
	}
	if *task == "" && *taskFile == "" {
		return errors.New("one of --task or --task-file is required")
	}
	if *taskFile != "" {
		data, err := os.ReadFile(*taskFile)
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		*task = string(data)
	}
	if *stateRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*stateRoot = filepath.Join(home, ".nox", "runs")
	}
	output := io.Writer(os.Stdout)
	if *jsonOutput {
		output = io.Discard
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	orchestrator := run.New()
	result, err := orchestrator.Launch(ctx, run.Config{
		Repo: *repo, From: *from, Intent: intent, OutputBranch: *outputBranch,
		Task: *task, Validation: *validate, Network: *network,
		Image: *image, StateRoot: *stateRoot, CodexHome: *codexHome, CPU: *cpu, Memory: *memory,
		PIDs: *pids, Timeout: *timeout, Output: output, ErrorOutput: os.Stderr,
		OnStart: func(metadata store.Metadata) error {
			_, err := fmt.Fprintf(os.Stderr, "run: %s\nmonitor: nox ui\ninspect: nox inspect %s\n", metadata.RunID, metadata.RunID)
			return err
		},
	})
	if *jsonOutput {
		return printJSON(result, err)
	}
	if err != nil {
		return err
	}
	if result.Metadata.Intent == string(run.IntentTest) {
		fmt.Printf("completed: test passed; evidence retained under %s\n", filepath.Dir(result.Metadata.Workspace))
		return nil
	}
	if result.NoChanges {
		fmt.Printf("completed: no changes; no branch created\n")
		return nil
	}
	fmt.Printf("completed: created local branch %s at %s\nnext: git switch %s\n", result.Metadata.OutputBranch, result.Metadata.ResultSHA, result.Metadata.OutputBranch)
	return nil
}

func inspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	remoteMode := fs.Bool("remote", false, "inspect a remote run using NOX_REMOTE_URL")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: nox inspect [--remote] <run-id>")
	}
	if *remoteMode {
		client, err := remote.NewClient(os.Getenv("NOX_REMOTE_URL"), os.Getenv("NOX_API_TOKEN"), nil)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return inspectRemote(ctx, client, fs.Arg(0), os.Stdout)
	}
	st, err := defaultStore()
	if err != nil {
		return err
	}
	metadata, err := st.ReadMetadata(fs.Arg(0))
	if err != nil {
		return err
	}
	return writeIndentedJSON(os.Stdout, metadata)
}

func inspectRemote(ctx context.Context, client *remote.Client, runID string, output io.Writer) error {
	status, err := client.Status(ctx, runID)
	if err != nil {
		return err
	}
	return writeIndentedJSON(output, status)
}

func writeIndentedJSON(output io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode inspection result: %w", err)
	}
	if _, err := fmt.Fprintln(output, string(data)); err != nil {
		return fmt.Errorf("write inspection result: %w", err)
	}
	return nil
}

func diff(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: nox diff <run-id>")
	}
	st, err := defaultStore()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(st.RunDir(args[0]), "changes.patch"))
	if err != nil {
		return fmt.Errorf("read diff: %w", err)
	}
	_, err = os.Stdout.Write(data)
	return err
}

func cleanup(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stale := fs.Bool("stale", false, "remove all labeled Nox containers")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *stale {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		count, err := (sandbox.Docker{Runner: execx.Runner{}}).CleanupStale(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d managed resource(s)\n", count)
		return nil
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: nox cleanup <run-id> or nox cleanup --stale")
	}
	st, err := defaultStore()
	if err != nil {
		return err
	}
	metadata, readErr := st.ReadMetadata(fs.Arg(0))
	if readErr == nil {
		docker := sandbox.Docker{Runner: execx.Runner{}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if metadata.ContainerID != "" {
			if removeErr := docker.Remove(ctx, sandbox.Container{ID: metadata.ContainerID}); removeErr != nil {
				cancel()
				return removeErr
			}
		}
		for _, volume := range []string{metadata.WorkspaceVolume, metadata.BaselineVolume, metadata.CodexVolume} {
			if volume == "" {
				continue
			}
			if removeErr := docker.RemoveVolume(ctx, volume); removeErr != nil {
				cancel()
				return removeErr
			}
		}
		cancel()
	}
	if err := st.RemoveRun(fs.Arg(0)); err != nil {
		return err
	}
	fmt.Printf("removed run %s\n", fs.Arg(0))
	return nil
}

func defaultStore() (store.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return store.Store{}, err
	}
	return store.New(filepath.Join(home, ".nox", "runs")), nil
}

func printJSON(result run.Result, runErr error) error {
	payload := struct {
		Result run.Result `json:"result"`
		Error  string     `json:"error,omitempty"`
	}{Result: result}
	if runErr != nil {
		payload.Error = runErr.Error()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return runErr
}
