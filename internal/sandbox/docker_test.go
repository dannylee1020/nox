package sandbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nox-dev/nox/internal/execx"
)

type fakeRunner struct {
	commands   []execx.Command
	doctorInfo *execx.Result
	doctorErr  error
}

func (f *fakeRunner) Run(_ context.Context, command execx.Command) (execx.Result, error) {
	f.commands = append(f.commands, command)
	result := execx.Result{ExitCode: 0}
	if len(command.Args) > 0 && command.Args[0] == "info" {
		if f.doctorInfo != nil || f.doctorErr != nil {
			if f.doctorInfo != nil {
				result = *f.doctorInfo
			}
			return result, f.doctorErr
		}
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
		"type=volume,src=nox-abc123-workspace,dst=/workspace", "type=volume,src=nox-abc123-codex,dst=/home/nox/.codex,volume-nocopy",
		"--tmpfs /tmp:rw,exec,nosuid,size=256m", "--tmpfs /var/tmp:rw,exec,nosuid,size=512m",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "--privileged") || strings.Contains(args, "--cap-add") || strings.Contains(args, "--cap-drop") || strings.Contains(args, "--network host") || strings.Contains(args, "docker.sock") {
		t.Fatalf("forbidden guardrail in args: %s", args)
	}
	if strings.Contains(args, ".codex-ro") || strings.Contains(args, "dst=/home/nox/.codex,readonly") {
		t.Fatalf("Codex credentials must not be mounted before repository setup: %s", args)
	}
}

func TestRepositorySetupProtectsWorkspace(t *testing.T) {
	fake := &fakeRunner{}
	_, err := (Docker{Runner: fake}).RunRepositorySetup(context.Background(), Container{ID: "container-id"}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(fake.commands[0].Args, " ")
	for _, want := range []string{"sh /workspace/.nox/setup.sh", "git status --porcelain --untracked-files=all", "git rev-parse HEAD", "repository setup changed tracked or non-ignored workspace files", "exit 86"} {
		if !strings.Contains(args, want) {
			t.Errorf("setup command missing %q: %s", want, args)
		}
	}
}

func TestPrepareCodexHomeCopiesCredentialsIntoWritableVolume(t *testing.T) {
	fake := &fakeRunner{}
	err := (Docker{Runner: fake}).PrepareCodexHome(context.Background(), Container{ID: "container-id"}, "/host/codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.commands) != 2 {
		t.Fatalf("commands = %d, want two", len(fake.commands))
	}
	if got := strings.Join(fake.commands[0].Args, " "); got != "cp /host/codex/. container-id:/home/nox/.codex" {
		t.Fatalf("copy command = %q", got)
	}
	if got := strings.Join(fake.commands[1].Args, " "); !strings.Contains(got, "exec -i container-id chown -R 1000:1000 /home/nox/.codex") {
		t.Fatalf("ownership command = %q", got)
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

func TestDoctorReportsDockerRuntimeFailures(t *testing.T) {
	tests := []struct {
		name       string
		info       execx.Result
		runErr     error
		want       string
		wantAbsent string
	}{
		{
			name:   "command failure",
			runErr: errors.New("permission denied"),
			want:   "Docker is unavailable",
		},
		{
			name:       "null output with socket denial",
			info:       execx.Result{Stdout: "null\n", Stderr: "permission denied connecting to Docker socket\n"},
			want:       "Docker is unavailable or inaccessible: permission denied connecting to Docker socket",
			wantAbsent: "runsc is not registered",
		},
		{
			name:       "empty output",
			info:       execx.Result{},
			want:       "Docker is unavailable or inaccessible: docker info returned no runtime data",
			wantAbsent: "runsc is not registered",
		},
		{
			name:       "malformed output",
			info:       execx.Result{Stdout: "{"},
			want:       "Docker returned invalid runtime data",
			wantAbsent: "runsc is not registered",
		},
		{
			name: "missing runsc",
			info: execx.Result{Stdout: `{"runc":{"status":"ok"}}`},
			want: "Docker runtime runsc is not registered",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := test.info
			fake := &fakeRunner{doctorInfo: &info, doctorErr: test.runErr}
			err := (Docker{Runner: fake}).Doctor(context.Background(), "nox-runner:v0")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("doctor error = %v, want containing %q", err, test.want)
			}
			if test.wantAbsent != "" && strings.Contains(err.Error(), test.wantAbsent) {
				t.Fatalf("doctor error = %v, should not contain %q", err, test.wantAbsent)
			}
			if len(fake.commands) != 1 {
				t.Fatalf("doctor commands = %d, want one", len(fake.commands))
			}
		})
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
