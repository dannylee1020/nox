package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/run"
	"github.com/nox-dev/nox/internal/sandbox"
	"github.com/nox-dev/nox/internal/store"
)

const usageText = `Nox runs a coding agent inside a local Docker/gVisor sandbox.

Commands:
  nox doctor
  nox launch --repo . --from main --output-branch nox/change --agent codex --task "..." --validate "..."
  nox inspect <run-id>
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
	case "doctor":
		err = doctor(os.Args[2:])
	case "launch":
		err = launch(os.Args[2:])
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

func launch(args []string) error {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", ".", "local Git repository")
	from := fs.String("from", "", "local branch or ref to launch from")
	outputBranch := fs.String("output-branch", "", "new local branch to create after validation")
	agentName := fs.String("agent", "codex", "agent: codex or generic")
	agentCommand := fs.String("agent-command", "", "generic agent command run inside the sandbox")
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
	orchestrator := run.New()
	result, err := orchestrator.Launch(context.Background(), run.Config{
		Repo: *repo, From: *from, OutputBranch: *outputBranch, Agent: *agentName,
		AgentCommand: *agentCommand, Task: *task, Validation: *validate, Network: *network,
		Image: *image, StateRoot: *stateRoot, CodexHome: *codexHome, CPU: *cpu, Memory: *memory,
		PIDs: *pids, Timeout: *timeout, Output: output, ErrorOutput: os.Stderr,
	})
	if *jsonOutput {
		return printJSON(result, err)
	}
	if err != nil {
		return err
	}
	if result.NoChanges {
		fmt.Printf("completed: no changes; no branch created\nrun: %s\n", result.Metadata.RunID)
		return nil
	}
	fmt.Printf("completed: created local branch %s at %s\nrun: %s\nnext: git switch %s\n", result.Metadata.OutputBranch, result.Metadata.ResultSHA, result.Metadata.RunID, result.Metadata.OutputBranch)
	return nil
}

func inspect(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: nox inspect <run-id>")
	}
	st, err := defaultStore()
	if err != nil {
		return err
	}
	metadata, err := st.ReadMetadata(args[0])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(metadata, "", "  ")
	fmt.Println(string(data))
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
		for _, volume := range []string{metadata.WorkspaceVolume, metadata.CodexVolume} {
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
