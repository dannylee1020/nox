package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/sandbox"
)

type PromptContext struct {
	Task       string
	BaseSHA    string
	Validation string
}

type Adapter interface {
	Name() string
	Environment() map[string]string
	Command() []string
	PermissionMode() string
	Prompt(PromptContext) string
}

type Codex struct{}

func (Codex) Name() string { return "codex" }

// PermissionMode is enforced by the CLI flag in Command; the outer Nox runsc
// container remains the isolation boundary.
func (Codex) PermissionMode() string { return "danger-full-access" }
func (Codex) Environment() map[string]string {
	return map[string]string{"HOME": "/home/nox", "CODEX_HOME": "/home/nox/.codex"}
}
func (Codex) Command() []string {
	return []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "--ephemeral", "-"}
}

func (Codex) Prompt(context PromptContext) string {
	return fmt.Sprintf(`# Nox sandbox execution envelope v1

Execute and test the delegated contract below.
Do not revisit established product or architecture decisions.
Use implementation judgment only where flexibility remains.
Report blockers instead of inventing material decisions.

Workspace: /workspace
Base commit: %s
Required validation: %s
Nox will run this validation again after agent execution.

## Hydrated execution contract

%s`, context.BaseSHA, context.Validation, context.Task)
}

func Run(ctx context.Context, docker sandbox.Docker, container sandbox.Container, adapter Adapter, prompt PromptContext, stdout, stderr io.Writer) (execx.Result, error) {
	result, err := docker.Exec(ctx, container, adapter.Command(), strings.NewReader(adapter.Prompt(prompt)), stdout, stderr)
	if err != nil {
		return result, fmt.Errorf("agent %s: %w", adapter.Name(), err)
	}
	return result, nil
}
