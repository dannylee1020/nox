package agent

import (
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
	command := strings.Join(adapter.Command(), " ")
	for _, want := range []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "--ephemeral"} {
		if !strings.Contains(command, want) {
			t.Errorf("command missing %q: %s", want, command)
		}
	}
	if got := adapter.Environment()["CODEX_HOME"]; got != "/home/nox/.codex" {
		t.Fatalf("CODEX_HOME = %q", got)
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
