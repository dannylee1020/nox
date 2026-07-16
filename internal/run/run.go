package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nox-dev/nox/internal/agent"
	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/gitx"
	"github.com/nox-dev/nox/internal/sandbox"
	"github.com/nox-dev/nox/internal/store"
)

type Config struct {
	Repo         string
	From         string
	OutputBranch string
	Agent        string
	AgentCommand string
	Task         string
	Validation   string
	Network      string
	Image        string
	StateRoot    string
	CodexHome    string
	CPU          string
	Memory       string
	PIDs         int
	Timeout      time.Duration
	Output       io.Writer
	ErrorOutput  io.Writer
}

type Result struct {
	Metadata  store.Metadata
	NoChanges bool
}

type Orchestrator struct {
	Git    gitx.Git
	Docker sandbox.Docker
	Store  store.Store
}

func New() Orchestrator {
	home, _ := os.UserHomeDir()
	return Orchestrator{
		Git:    gitx.Git{Runner: execx.Runner{}},
		Docker: sandbox.Docker{Runner: execx.Runner{}},
		Store:  store.New(filepath.Join(home, ".nox", "runs")),
	}
}

func NewID() string {
	var data [5]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}

func (o Orchestrator) Launch(parent context.Context, config Config) (result Result, err error) {
	// apply defaults and validate the launch request.
	if config.Output == nil {
		config.Output = io.Discard
	}
	if config.ErrorOutput == nil {
		config.ErrorOutput = io.Discard
	}
	if config.Timeout <= 0 {
		config.Timeout = 2 * time.Hour
	}
	if config.Network == "" {
		config.Network = "online"
	}
	if config.Image == "" {
		config.Image = "nox-runner:v0"
	}
	if config.CPU == "" {
		config.CPU = "2"
	}
	if config.Memory == "" {
		config.Memory = "4g"
	}
	if config.PIDs == 0 {
		config.PIDs = 256
	}
	if config.StateRoot != "" {
		o.Store = store.New(config.StateRoot)
	}
	if strings.TrimSpace(config.From) == "" || strings.TrimSpace(config.OutputBranch) == "" {
		return result, fmt.Errorf("--from and --output-branch are required")
	}
	if strings.TrimSpace(config.Validation) == "" {
		return result, fmt.Errorf("--validate is required")
	}
	adapterConfig := agent.Config{Name: config.Agent, Command: config.AgentCommand}
	if adapterConfig.Name == "" {
		adapterConfig.Name = "codex"
	}
	adapter, err := agent.New(adapterConfig)
	if err != nil {
		return result, err
	}

	//create the run context and durable run metadata.
	ctx, cancel := context.WithTimeout(parent, config.Timeout)
	defer cancel()
	id := NewID()
	runDir, err := o.Store.Ensure(id)
	if err != nil {
		return result, err
	}
	workspace := filepath.Join(runDir, "workspace")
	metadata := store.Metadata{
		RunID: id, Repo: config.Repo, From: config.From, OutputBranch: config.OutputBranch,
		Agent: adapter.Name(), Validation: config.Validation, Network: config.Network,
		Image: config.Image, State: store.StateInitializing, StartedAt: time.Now(), Workspace: workspace,
	}
	writeState := func(state store.State) {
		metadata.State = state
		_ = o.Store.WriteMetadata(metadata)
	}
	fail := func(failErr error) (Result, error) {
		if IsCancelled(failErr) {
			metadata.State = store.StateCancelled
		} else {
			metadata.State = store.StateFailed
		}
		metadata.Error = failErr.Error()
		metadata.CompletedAt = time.Now()
		metadata.Retained = true
		_ = o.Store.WriteMetadata(metadata)
		return Result{Metadata: metadata}, failErr
	}
	writeState(store.StateInitializing)

	// resolve the source repository and clone the exact base commit.
	root, err := o.Git.RepoRoot(ctx, config.Repo)
	if err != nil {
		return fail(err)
	}
	config.Repo = root
	metadata.Repo = root
	metadata.Warning = ""
	if dirty, dirtyErr := o.Git.Dirty(ctx, root); dirtyErr == nil && dirty {
		metadata.Warning = "source worktree has uncommitted changes; they were excluded"
		fmt.Fprintln(config.ErrorOutput, "warning: source worktree has uncommitted changes; they will be excluded")
	}
	baseSHA, err := o.Git.ResolveCommit(ctx, root, config.From)
	if err != nil {
		return fail(err)
	}
	metadata.BaseSHA = baseSHA
	if err := o.Git.ValidateBranch(ctx, root, config.OutputBranch); err != nil {
		return fail(err)
	}

	writeState(store.StateCloning)
	if err := o.Git.CloneAt(ctx, root, baseSHA, workspace); err != nil {
		return fail(err)
	}

	codexHome := config.CodexHome
	if adapter.Name() == "codex" && codexHome == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return fail(homeErr)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	if adapter.Name() == "codex" {
		if _, statErr := os.Stat(codexHome); statErr != nil {
			return fail(fmt.Errorf("Codex auth directory %q is unavailable: %w", codexHome, statErr))
		}
	}

	// create the isolated runsc container.
	container, err := o.Docker.Create(ctx, sandbox.Config{
		Image: config.Image, Workspace: workspace, RunID: id, Network: config.Network,
		CPU: config.CPU, Memory: config.Memory, PIDs: config.PIDs, CodexHome: codexHome,
		Environment: adapter.Environment(),
	})
	if err != nil {
		return fail(err)
	}
	metadata.ContainerID = container.ID
	writeState(store.StateStarting)
	// Teardown runs on every path after the container is created.
	defer func() {
		terminalState := metadata.State
		metadata.State = store.StateTeardown
		_ = o.Store.WriteMetadata(metadata)
		removeErr := o.Docker.Remove(context.Background(), container)
		if removeErr != nil && err == nil {
			err = removeErr
			metadata.Error = removeErr.Error()
			terminalState = store.StateFailed
			metadata.Retained = true
		}
		metadata.State = terminalState
		_ = o.Store.WriteMetadata(metadata)
	}()
	if err := o.Docker.Start(ctx, container); err != nil {
		return fail(err)
	}

	// run the selected agent inside the sandbox.
	agentLog, err := o.Store.OpenLog(id, "agent.log")
	if err != nil {
		return fail(err)
	}
	defer agentLog.Close()
	agentOut := io.MultiWriter(agentLog, config.Output)
	writeState(store.StateAgentRunning)
	agentResult, err := agent.Run(ctx, o.Docker, container, adapter, strings.NewReader(config.Task), agentOut, agentLog)
	metadata.ExitCode = agentResult.ExitCode
	if err != nil || agentResult.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("agent exited with code %d", agentResult.ExitCode)
		}
		return fail(err)
	}

	// validate the changes in the same sandbox container.
	validationLog, err := o.Store.OpenLog(id, "validation.log")
	if err != nil {
		return fail(err)
	}
	defer validationLog.Close()
	writeState(store.StateValidating)
	validationOut := io.MultiWriter(validationLog, config.Output)
	validationResult, validationErr := o.Docker.Exec(ctx, container, []string{"sh", "-lc", config.Validation}, nil, validationOut, validationLog)
	metadata.ValidationCode = validationResult.ExitCode
	if validationErr != nil || validationResult.ExitCode != 0 {
		if validationErr == nil {
			validationErr = fmt.Errorf("validation exited with code %d", validationResult.ExitCode)
		}
		return fail(validationErr)
	}

	// publish only validated changes as a local host-side branch.
	writeState(store.StatePublishing)
	commitMessage := "nox: " + summarizeTask(config.Task)
	resultSHA, changed, publishErr := o.Git.Publish(ctx, root, baseSHA, config.OutputBranch, workspace, commitMessage)
	if publishErr != nil {
		return fail(publishErr)
	}
	if !changed {
		// a successful no-change run does not create a result branch.
		metadata.State = store.StateCompleted
		metadata.CompletedAt = time.Now()
		metadata.Retained = false
		_ = o.Store.WriteText(id, "changes.patch", "")
		_ = o.Store.WriteMetadata(metadata)
		_ = o.Store.RemoveWorkspace(id)
		result.Metadata = metadata
		result.NoChanges = true
		return result, nil
	}
	// save the result evidence and remove the successful workspace.
	metadata.ResultSHA = resultSHA
	metadata.State = store.StateCompleted
	metadata.CompletedAt = time.Now()
	metadata.Retained = false
	if patch, patchErr := o.Git.CommitPatch(ctx, root, baseSHA, resultSHA); patchErr == nil {
		_ = o.Store.WriteText(id, "changes.patch", patch)
	}
	if err := o.Store.WriteMetadata(metadata); err != nil {
		return result, err
	}
	_ = o.Store.RemoveWorkspace(id)
	result.Metadata = metadata
	return result, nil
}

func summarizeTask(task string) string {
	text := strings.Join(strings.Fields(task), " ")
	if len(text) > 60 {
		text = text[:57] + "..."
	}
	if text == "" {
		return "sandbox changes"
	}
	return text
}

func IsCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
