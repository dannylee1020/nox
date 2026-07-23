package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestCodexAdapter(t *testing.T) {
	adapter := Codex{}
	if adapter.Name() != "codex" {
		t.Fatalf("name = %q", adapter.Name())
	}
	if adapter.PermissionMode() != "danger-full-access" {
		t.Fatalf("permission mode = %q", adapter.PermissionMode())
	}
	wantCommand := []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "--ephemeral", "-"}
	if got := adapter.Command(); !reflect.DeepEqual(got, wantCommand) {
		t.Fatalf("command = %#v, want %#v", got, wantCommand)
	}
	if got := adapter.Environment()["CODEX_HOME"]; got != "/home/nox/.codex" {
		t.Fatalf("CODEX_HOME = %q", got)
	}
}

func TestCodexPromptAddsExecutionEnvelopeWithoutRewritingContract(t *testing.T) {
	contract := "# Nox execution contract v1\n\n## Context and extra\n\n100% preserved: do not normalize this.\n"
	got := (Codex{}).Prompt(PromptContext{
		Task: contract, BaseSHA: "0123456789abcdef", Validation: "go test ./... && printf '100%%'",
	})
	for _, want := range []string{
		"# Nox sandbox execution envelope v1",
		"Base commit: 0123456789abcdef",
		"Required validation: go test ./... && printf '100%%'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q: %s", want, got)
		}
	}
	if !strings.HasSuffix(got, contract) {
		t.Fatalf("prompt does not preserve contract as its final payload: %q", got)
	}
}

func TestCodexPromptGivesTestModeOnlyTesterResponsibility(t *testing.T) {
	got := (Codex{}).Prompt(PromptContext{
		Task: "test contract", BaseSHA: "base", Validation: "npm run e2e", Intent: "test",
	})
	for _, want := range []string{
		"Act only as an end-to-end tester",
		"Do not implement fixes",
		"Imitate the real-world user workflow",
		"Nox will run the required validation independently",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tester prompt missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "create a branch") {
		t.Fatalf("tester prompt included publication guidance: %s", got)
	}
}
