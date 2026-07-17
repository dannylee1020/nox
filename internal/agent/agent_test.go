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
