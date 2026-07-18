package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestCodexAdapter(t *testing.T) {
	adapter, err := New(Config{Name: "codex"})
	if err != nil {
		t.Fatal(err)
	}
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
	generic, err := New(Config{Name: "generic", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if generic.PermissionMode() != "outer-sandbox" {
		t.Fatalf("generic permission mode = %q", generic.PermissionMode())
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

func TestGenericPromptPreservesTask(t *testing.T) {
	contract := "arbitrary task\n\n## Context and extra\n% complete\n"
	if got := (Generic{Cmd: "true"}).Prompt(PromptContext{Task: contract, BaseSHA: "ignored", Validation: "ignored"}); got != contract {
		t.Fatalf("generic prompt = %q, want %q", got, contract)
	}
}

func TestGenericAdapterRequiresCommand(t *testing.T) {
	if _, err := New(Config{Name: "generic"}); err == nil {
		t.Fatal("expected missing generic command error")
	}
	adapter, err := New(Config{Name: "generic", Command: "printf ok"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(adapter.Command(), " "); got != "sh -lc printf ok" {
		t.Fatalf("generic command = %q", got)
	}
}
