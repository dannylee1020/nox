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
	Intent     string
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
	role := `Execute and test the delegated contract below.
Do not revisit established product or architecture decisions.
Use implementation judgment only where flexibility remains.
Report blockers instead of inventing material decisions.`
	if context.Intent == "test" {
		role = `Act only as an end-to-end tester for the existing implementation.
Do not implement fixes, edit tracked source, rewrite tests, or change tracked configuration.
You may install dependencies, create runtime state, start services, seed data, and use temporary or ignored paths as needed.
Imitate the real-world user workflow described by the contract, observe the outcome, and report findings.
Do not claim success from your own narrative; Nox will run the required validation independently.`
	}
	return fmt.Sprintf(`# Nox sandbox execution envelope v1

%s

Workspace: /workspace
Base commit: %s
Required validation: %s
Nox will run this validation again after agent execution.

## Hydrated execution contract

%s`, role, context.BaseSHA, context.Validation, context.Task)
}

func Run(ctx context.Context, docker sandbox.Docker, container sandbox.Container, adapter Adapter, prompt PromptContext, stdout, stderr io.Writer) (execx.Result, error) {
	result, err := docker.Exec(ctx, container, adapter.Command(), strings.NewReader(adapter.Prompt(prompt)), stdout, stderr)
	if err != nil {
		return result, fmt.Errorf("agent %s: %w", adapter.Name(), err)
	}
	return result, nil
}
