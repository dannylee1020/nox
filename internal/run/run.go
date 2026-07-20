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
	RunID        string
	Repo         string
	From         string
	OutputBranch string
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
	OnStart      func(store.Metadata) error
}

type Result struct {
	Metadata  store.Metadata
	NoChanges bool
}

type Orchestrator struct {
	Git     gitx.Git
	Docker  sandbox.Docker
	Store   store.Store
	Adapter agent.Adapter
}

func New() Orchestrator {
	home, _ := os.UserHomeDir()
	return Orchestrator{
		Git:     gitx.Git{Runner: execx.Runner{}},
		Docker:  sandbox.Docker{Runner: execx.Runner{}},
		Store:   store.New(filepath.Join(home, ".nox", "runs")),
		Adapter: agent.Codex{},
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
	adapter := o.Adapter
	if adapter == nil {
		adapter = agent.Codex{}
	}

	//create the run context and durable run metadata.
	ctx, cancel := context.WithTimeout(parent, config.Timeout)
	defer cancel()
	id := config.RunID
	if id == "" {
		id = NewID()
	} else if !validRunID(id) {
		return result, fmt.Errorf("invalid run ID %q", id)
	}
	runDir, err := o.Store.Ensure(id)
	if err != nil {
		return result, err
	}
	workspace := filepath.Join(runDir, "workspace")
	metadata := store.Metadata{
		RunID: id, Repo: config.Repo, From: config.From, OutputBranch: config.OutputBranch,
		Agent: adapter.Name(), AgentPermissions: adapter.PermissionMode(), Validation: config.Validation,
		Network: config.Network, Image: config.Image, State: store.StateInitializing,
		StartedAt: time.Now(), Workspace: workspace,
	}
	var workspaceVolume string
	var codexVolume string
	var workspaceExported bool
	var container sandbox.Container
	var terminalState store.State
	exportWorkspace := func() error {
		if workspaceVolume == "" || workspaceExported {
			return nil
		}
		exportCtx, exportCancel := context.WithTimeout(context.Background(), time.Minute)
		defer exportCancel()
		temporaryWorkspace := workspace + ".exporting"
		if err := o.Docker.ExportWorkspace(exportCtx, workspaceVolume, config.Image, id, temporaryWorkspace); err != nil {
			return err
		}
		if err := os.RemoveAll(workspace); err != nil {
			return fmt.Errorf("replace host workspace: %w", err)
		}
		if err := os.Rename(temporaryWorkspace, workspace); err != nil {
			return fmt.Errorf("install exported workspace: %w", err)
		}
		metadata.WorkspaceExported = true
		_ = o.Store.WriteMetadata(metadata)
		workspaceExported = true
		return nil
	}
	writeState := func(state store.State) error {
		metadata.State = state
		return o.Store.WriteMetadata(metadata)
	}
	fail := func(failErr error) (Result, error) {
		if workspaceVolume != "" && !workspaceExported {
			if exportErr := exportWorkspace(); exportErr != nil {
				failErr = fmt.Errorf("%w; export workspace: %v", failErr, exportErr)
			}
		}
		if IsCancelled(failErr) {
			terminalState = store.StateCancelled
		} else {
			terminalState = store.StateFailed
		}
		metadata.Error = failErr.Error()
		metadata.Retained = true
		_ = o.Store.WriteMetadata(metadata)
		return Result{Metadata: metadata}, failErr
	}
	if err := writeState(store.StateInitializing); err != nil {
		return result, fmt.Errorf("write initial run metadata: %w", err)
	}
	defer func() {
		if terminalState == "" {
			if err != nil {
				if IsCancelled(err) {
					terminalState = store.StateCancelled
				} else {
					terminalState = store.StateFailed
				}
			} else {
				terminalState = metadata.State
			}
		}
		metadata.State = store.StateTeardown
		_ = o.Store.WriteMetadata(metadata)
		teardownCtx, teardownCancel := context.WithTimeout(context.Background(), time.Minute)
		defer teardownCancel()
		if container.ID != "" {
			if removeErr := o.Docker.Remove(teardownCtx, container); removeErr != nil {
				if err == nil {
					err = removeErr
					terminalState = store.StateFailed
					metadata.Retained = true
				}
				metadata.Error = appendError(metadata.Error, removeErr)
			}
		}
		if workspaceVolume != "" {
			if removeErr := o.Docker.RemoveVolume(teardownCtx, workspaceVolume); removeErr != nil {
				if err == nil {
					err = removeErr
					terminalState = store.StateFailed
					metadata.Retained = true
				}
				metadata.Error = appendError(metadata.Error, removeErr)
			}
		}
		if codexVolume != "" {
			if removeErr := o.Docker.RemoveVolume(teardownCtx, codexVolume); removeErr != nil {
				if err == nil {
					err = removeErr
					terminalState = store.StateFailed
					metadata.Retained = true
				}
				metadata.Error = appendError(metadata.Error, removeErr)
			}
		}
		if metadata.CompletedAt.IsZero() {
			metadata.CompletedAt = time.Now()
		}
		metadata.State = terminalState
		_ = o.Store.WriteMetadata(metadata)
		result.Metadata = metadata
	}()
	if config.OnStart != nil {
		if err := config.OnStart(metadata); err != nil {
			return fail(fmt.Errorf("announce run %s: %w", id, err))
		}
	}

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
	identity, err := o.Git.Identity(ctx, root)
	if err != nil {
		return fail(err)
	}
	metadata.CommitAuthor = identity.Name + " <" + identity.Email + ">"
	if err := o.Store.WriteMetadata(metadata); err != nil {
		return fail(err)
	}

	if err := writeState(store.StateCloning); err != nil {
		return fail(err)
	}
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

	workspaceVolume, err = o.Docker.CreateWorkspaceVolume(ctx, id)
	if err != nil {
		return fail(err)
	}
	metadata.WorkspaceVolume = workspaceVolume
	_ = o.Store.WriteMetadata(metadata)

	if err := o.Docker.SeedWorkspace(ctx, workspaceVolume, workspace, config.Image, id); err != nil {
		return fail(err)
	}
	if adapter.Name() == "codex" {
		// Mount an empty writable home now; credentials are copied only after repository setup succeeds.
		codexVolume, err = o.Docker.CreateCodexVolume(ctx, id)
		if err != nil {
			return fail(err)
		}
		metadata.CodexVolume = codexVolume
		_ = o.Store.WriteMetadata(metadata)
	}

	// create the isolated runsc container.
	container, err = o.Docker.Create(ctx, sandbox.Config{
		Image: config.Image, WorkspaceVolume: workspaceVolume, CodexHomeVolume: codexVolume,
		RunID: id, Network: config.Network, CPU: config.CPU, Memory: config.Memory, PIDs: config.PIDs,
		Environment: adapter.Environment(),
	})
	if err != nil {
		return fail(err)
	}
	metadata.ContainerID = container.ID
	if err := writeState(store.StateStarting); err != nil {
		return fail(err)
	}
	if err := o.Docker.Start(ctx, container); err != nil {
		return fail(err)
	}
	if err := o.Docker.PrepareWorkspace(ctx, container); err != nil {
		return fail(err)
	}
	hasSetup, err := o.Docker.HasRepositorySetup(ctx, container)
	if err != nil {
		return fail(err)
	}
	if hasSetup {
		setupLog, openErr := o.Store.OpenLog(id, "setup.log")
		if openErr != nil {
			return fail(openErr)
		}
		if err := writeState(store.StateSettingUp); err != nil {
			_ = setupLog.Close()
			return fail(err)
		}
		setupOut := io.MultiWriter(setupLog, config.Output)
		setupResult, setupErr := o.Docker.RunRepositorySetup(ctx, container, setupOut, setupLog)
		closeErr := setupLog.Close()
		if setupErr != nil || setupResult.ExitCode != 0 {
			if setupErr != nil {
				setupErr = fmt.Errorf("repository setup failed with code %d: %w", setupResult.ExitCode, setupErr)
			} else {
				setupErr = fmt.Errorf("repository setup exited with code %d", setupResult.ExitCode)
			}
			return fail(setupErr)
		}
		if closeErr != nil {
			return fail(fmt.Errorf("close setup log: %w", closeErr))
		}
	}
	if codexVolume != "" {
		if err := o.Docker.PrepareCodexHome(ctx, container, codexHome); err != nil {
			return fail(err)
		}
	}

	// run the selected agent inside the sandbox.
	agentLog, err := o.Store.OpenLog(id, "agent.log")
	if err != nil {
		return fail(err)
	}
	defer agentLog.Close()
	agentOut := io.MultiWriter(agentLog, config.Output)
	if err := writeState(store.StateAgentRunning); err != nil {
		return fail(err)
	}
	agentResult, err := agent.Run(ctx, o.Docker, container, adapter, agent.PromptContext{
		Task: config.Task, BaseSHA: metadata.BaseSHA, Validation: config.Validation,
	}, agentOut, agentLog)
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
	if err := writeState(store.StateValidating); err != nil {
		return fail(err)
	}
	validationOut := io.MultiWriter(validationLog, config.Output)
	validationResult, validationErr := o.Docker.Exec(ctx, container, []string{"sh", "-lc", config.Validation}, nil, validationOut, validationLog)
	metadata.ValidationCode = validationResult.ExitCode
	if validationErr != nil || validationResult.ExitCode != 0 {
		if validationErr == nil {
			validationErr = fmt.Errorf("validation exited with code %d", validationResult.ExitCode)
		}
		return fail(validationErr)
	}

	if err := exportWorkspace(); err != nil {
		return fail(err)
	}

	// publish only validated changes as a local host-side branch.
	if err := writeState(store.StatePublishing); err != nil {
		return fail(err)
	}
	commitMessage := "nox: " + summarizeTask(config.Task)
	resultSHA, changed, publishErr := o.Git.Publish(ctx, root, baseSHA, config.OutputBranch, workspace, commitMessage, identity)
	if publishErr != nil {
		return fail(publishErr)
	}
	if !changed {
		// a successful no-change run does not create a result branch.
		terminalState = store.StateCompleted
		metadata.Retained = false
		_ = o.Store.WriteText(id, "changes.patch", "")
		_ = o.Store.RemoveWorkspace(id)
		result.Metadata = metadata
		result.NoChanges = true
		return result, nil
	}
	// save the result evidence and remove the successful workspace.
	metadata.ResultSHA = resultSHA
	terminalState = store.StateCompleted
	metadata.Retained = false
	if patch, patchErr := o.Git.CommitPatch(ctx, root, baseSHA, resultSHA); patchErr == nil {
		_ = o.Store.WriteText(id, "changes.patch", patch)
	}
	_ = o.Store.RemoveWorkspace(id)
	result.Metadata = metadata
	return result, nil
}

func validRunID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
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

func appendError(existing string, err error) string {
	if existing == "" {
		return err.Error()
	}
	return existing + "; " + err.Error()
}
