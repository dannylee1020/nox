package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/nox-dev/nox/internal/execx"
)

type fakeRunner struct {
	commands []execx.Command
}

func (f *fakeRunner) Run(_ context.Context, command execx.Command) (execx.Result, error) {
	f.commands = append(f.commands, command)
	result := execx.Result{ExitCode: 0}
	if len(command.Args) > 0 && command.Args[0] == "info" {
		result.Stdout = `{"runsc":{"status":"ok"}}`
	}
	if len(command.Args) > 0 && command.Args[0] == "create" {
		result.Stdout = "container-id\n"
	}
	if len(command.Args) > 1 && command.Args[0] == "volume" && command.Args[1] == "create" {
		result.Stdout = "volume-id\n"
	}
	if len(command.Args) > 0 && command.Args[0] == "ps" {
		result.Stdout = "old-container\n"
	}
	return result, nil
}

func (f *fakeRunner) Stream(_ context.Context, command execx.Command, _ io.Writer, _ io.Writer) (execx.Result, error) {
	f.commands = append(f.commands, command)
	return execx.Result{ExitCode: 0}, nil
}

func TestCreateWorkspaceVolumeUsesManagedLabels(t *testing.T) {
	fake := &fakeRunner{}
	volume, err := (Docker{Runner: fake}).CreateWorkspaceVolume(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if volume != "volume-id" {
		t.Fatalf("volume = %q", volume)
	}
	args := strings.Join(fake.commands[0].Args, " ")
	for _, want := range []string{"volume create", "io.nox.managed=true", "io.nox.run-id=abc123", "io.nox.kind=workspace", "nox-abc123-workspace"} {
		if !strings.Contains(args, want) {
			t.Errorf("volume command missing %q: %s", want, args)
		}
	}
}

func TestCreateUsesRunscAndMinimumGuardrails(t *testing.T) {
	fake := &fakeRunner{}
	docker := Docker{Runner: fake}
	container, err := docker.Create(context.Background(), Config{
		Image: "nox-runner:v0", WorkspaceVolume: "nox-abc123-workspace", RunID: "abc123", Network: "online",
		CPU: "2", Memory: "4g", PIDs: 256, CodexHomeVolume: "nox-abc123-codex",
		Environment: map[string]string{"HOME": "/home/nox", "CODEX_HOME": "/home/nox/.codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if container.ID != "container-id" {
		t.Fatalf("container id = %q", container.ID)
	}
	if len(fake.commands) != 1 {
		t.Fatalf("commands = %d, want one", len(fake.commands))
	}
	args := strings.Join(fake.commands[0].Args, " ")
	for _, want := range []string{
		"create", "--runtime runsc", "--user 0:0",
		"--security-opt no-new-privileges:true", "--network bridge", "--pids-limit 256",
		"type=volume,src=nox-abc123-workspace,dst=/workspace", "type=volume,src=nox-abc123-codex,dst=/home/nox/.codex-ro,readonly",
		"--tmpfs /tmp:rw,exec,nosuid,size=256m", "--tmpfs /var/tmp:rw,exec,nosuid,size=512m",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "--privileged") || strings.Contains(args, "--cap-add") || strings.Contains(args, "--cap-drop") || strings.Contains(args, "--network host") || strings.Contains(args, "docker.sock") {
		t.Fatalf("forbidden guardrail in args: %s", args)
	}
}

func TestDoctorRequiresRunsc(t *testing.T) {
	fake := &fakeRunner{}
	if err := (Docker{Runner: fake}).Doctor(context.Background(), "nox-runner:v0"); err != nil {
		t.Fatal(err)
	}
	if len(fake.commands) != 3 {
		t.Fatalf("doctor commands = %d, want three", len(fake.commands))
	}
}

func TestCleanupStaleUsesManagedLabel(t *testing.T) {
	fake := &fakeRunner{}
	count, err := (Docker{Runner: fake}).CleanupStale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("removed %d containers", count)
	}
	if !strings.Contains(strings.Join(fake.commands[0].Args, " "), "label=io.nox.managed=true") {
		t.Fatalf("missing managed label filter: %v", fake.commands[0].Args)
	}
}
