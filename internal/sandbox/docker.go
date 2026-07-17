package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/nox-dev/nox/internal/execx"
)

type Config struct {
	Image           string
	WorkspaceVolume string
	CodexHomeVolume string
	RunID           string
	Network         string
	CPU             string
	Memory          string
	PIDs            int
	Environment     map[string]string
}

type Docker struct {
	Runner execx.StreamRunner
}

type Container struct {
	ID     string
	Config Config
}

func WorkspaceVolumeName(runID string) string {
	return "nox-" + runID + "-workspace"
}

func CodexVolumeName(runID string) string {
	return "nox-" + runID + "-codex"
}

func (d Docker) Doctor(ctx context.Context, image string) error {
	info, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"info", "--format", "{{json .Runtimes}}"}})
	if err != nil {
		return fmt.Errorf("Docker is unavailable: %w", err)
	}
	if !strings.Contains(info.Stdout, "runsc") {
		return fmt.Errorf("Docker runtime runsc is not registered")
	}
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"image", "inspect", image}}); err != nil {
		return fmt.Errorf("runner image %q is unavailable: %w", image, err)
	}
	test, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{
		"run", "--rm", "--pull=never", "--runtime", "runsc", "--network", "none", "--pids-limit", "64", image, "true",
	}})
	if err != nil || test.ExitCode != 0 {
		return fmt.Errorf("runsc smoke container failed: %w", err)
	}
	return nil
}

func (d Docker) CreateWorkspaceVolume(ctx context.Context, runID string) (string, error) {
	return d.createManagedVolume(ctx, WorkspaceVolumeName(runID), runID, "workspace")
}

func (d Docker) CreateCodexVolume(ctx context.Context, runID string) (string, error) {
	return d.createManagedVolume(ctx, CodexVolumeName(runID), runID, "codex")
}

func (d Docker) createManagedVolume(ctx context.Context, name, runID, kind string) (string, error) {
	if strings.TrimSpace(runID) == "" {
		return "", fmt.Errorf("run id is required")
	}
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{
		"volume", "create",
		"--label", "io.nox.managed=true",
		"--label", "io.nox.run-id=" + runID,
		"--label", "io.nox.kind=" + kind,
		name,
	}})
	if err != nil {
		return "", fmt.Errorf("create %s volume: %w", kind, err)
	}
	volume := strings.TrimSpace(result.Stdout)
	if volume == "" {
		return "", fmt.Errorf("Docker returned an empty %s volume name", kind)
	}
	return volume, nil
}

func (d Docker) Create(ctx context.Context, config Config) (Container, error) {
	if config.Image == "" || config.WorkspaceVolume == "" || config.RunID == "" {
		return Container{}, fmt.Errorf("image, workspace volume, and run id are required")
	}
	if config.Network != "online" && config.Network != "none" {
		return Container{}, fmt.Errorf("network must be online or none")
	}
	if config.PIDs <= 0 {
		return Container{}, fmt.Errorf("pids limit must be positive")
	}
	network := "none"
	if config.Network == "online" {
		network = "bridge"
	}
	args := []string{
		"create",
		"--runtime", "runsc",
		"--name", "nox-" + config.RunID,
		"--label", "io.nox.managed=true",
		"--label", "io.nox.run-id=" + config.RunID,
		"--user", "1000:1000",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--cpus", config.CPU,
		"--memory", config.Memory,
		"--pids-limit", strconv.Itoa(config.PIDs),
		"--network", network,
		"--workdir", "/workspace",
		"--mount", "type=volume,src=" + config.WorkspaceVolume + ",dst=/workspace",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
		"--tmpfs", "/var/tmp:rw,exec,nosuid,size=512m",
	}
	if config.CodexHomeVolume != "" {
		args = append(args, "--mount", "type=volume,src="+config.CodexHomeVolume+",dst=/home/nox/.codex-ro,readonly")
	}
	keys := make([]string, 0, len(config.Environment))
	for key := range config.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+config.Environment[key])
	}
	args = append(args, config.Image, "sleep", "infinity")
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: args})
	if err != nil {
		return Container{}, fmt.Errorf("create gVisor container: %w", err)
	}
	id := strings.TrimSpace(result.Stdout)
	if id == "" {
		return Container{}, fmt.Errorf("Docker returned an empty container id")
	}
	return Container{ID: id, Config: config}, nil
}

func (d Docker) SeedWorkspace(ctx context.Context, volume, source, image, runID string) error {
	return d.seedDirectory(ctx, volume, source, image, runID, "workspace")
}

func (d Docker) SeedCodexHome(ctx context.Context, volume, source, image, runID string) error {
	return d.seedDirectory(ctx, volume, source, image, runID, "codex")
}

func (d Docker) seedDirectory(ctx context.Context, volume, source, image, runID, kind string) (err error) {
	if volume == "" || source == "" || image == "" || runID == "" {
		return fmt.Errorf("volume, source, image, and run id are required")
	}
	helper, err := d.createWorkspaceHelper(ctx, volume, image, runID, "seed-"+kind)
	if err != nil {
		return err
	}
	defer func() {
		removeErr := d.Remove(context.Background(), helper)
		if removeErr != nil && err == nil {
			err = removeErr
		}
	}()
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"cp", source + string(os.PathSeparator) + ".", helper.ID + ":/workspace"}}); err != nil {
		return fmt.Errorf("seed %s volume: %w", kind, err)
	}
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"start", helper.ID}}); err != nil {
		return fmt.Errorf("start %s seed helper: %w", kind, err)
	}
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"exec", helper.ID, "chown", "-R", "1000:1000", "/workspace"}}); err != nil {
		return fmt.Errorf("set %s ownership: %w", kind, err)
	}
	return nil
}

func (d Docker) ExportWorkspace(ctx context.Context, volume, image, runID, destination string) (err error) {
	if volume == "" || image == "" || runID == "" || destination == "" {
		return fmt.Errorf("volume, image, run id, and destination are required")
	}
	if err := os.RemoveAll(destination); err != nil {
		return fmt.Errorf("reset exported workspace: %w", err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create exported workspace: %w", err)
	}
	helper, err := d.createWorkspaceHelper(ctx, volume, image, runID, "export")
	if err != nil {
		return err
	}
	defer func() {
		removeErr := d.Remove(context.Background(), helper)
		if removeErr != nil && err == nil {
			err = removeErr
		}
	}()
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"cp", helper.ID + ":/workspace/.", destination}}); err != nil {
		return fmt.Errorf("export workspace: %w", err)
	}
	return nil
}

func (d Docker) createWorkspaceHelper(ctx context.Context, volume, image, runID, purpose string) (Container, error) {
	name := "nox-" + runID + "-" + purpose
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{
		"create",
		"--runtime", "runsc",
		"--name", name,
		"--label", "io.nox.managed=true",
		"--label", "io.nox.run-id=" + runID,
		"--label", "io.nox.kind=workspace-helper",
		"--user", "0:0",
		"--network", "none",
		"--mount", "type=volume,src=" + volume + ",dst=/workspace",
		image, "sleep", "infinity",
	}})
	if err != nil {
		return Container{}, fmt.Errorf("create workspace helper: %w", err)
	}
	id := strings.TrimSpace(result.Stdout)
	if id == "" {
		return Container{}, fmt.Errorf("Docker returned an empty workspace helper id")
	}
	return Container{ID: id}, nil
}

func (d Docker) Start(ctx context.Context, container Container) error {
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"start", container.ID}}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

func (d Docker) PrepareCodexHome(ctx context.Context, container Container) error {
	_, err := d.Exec(ctx, container, []string{"sh", "-lc", "cp -a /home/nox/.codex-ro/. /home/nox/.codex/"}, nil, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("prepare writable Codex home: %w", err)
	}
	return nil
}

func (d Docker) Exec(ctx context.Context, container Container, args []string, input io.Reader, stdout, stderr io.Writer) (execx.Result, error) {
	if len(args) == 0 {
		return execx.Result{}, fmt.Errorf("container command is required")
	}
	return d.Runner.Stream(ctx, execx.Command{
		Name: "docker",
		Args: append([]string{"exec", "-i", container.ID}, args...),
		In:   input,
	}, stdout, stderr)
}

func (d Docker) Remove(ctx context.Context, container Container) error {
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"rm", "-f", container.ID}})
	if err != nil && result.ExitCode != 1 {
		return fmt.Errorf("remove container: %w", err)
	}
	return nil
}

func (d Docker) RemoveVolume(ctx context.Context, volume string) error {
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"volume", "rm", volume}})
	if err != nil && result.ExitCode != 1 {
		return fmt.Errorf("remove workspace volume: %w", err)
	}
	return nil
}

func (d Docker) CleanupStale(ctx context.Context) (int, error) {
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"ps", "-aq", "--filter", "label=io.nox.managed=true"}})
	if err != nil {
		return 0, fmt.Errorf("list managed containers: %w", err)
	}
	count := 0
	for _, id := range strings.Fields(result.Stdout) {
		if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"rm", "-f", id}}); err != nil {
			return count, err
		}
		count++
	}
	volumes, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"volume", "ls", "-q", "--filter", "label=io.nox.managed=true"}})
	if err != nil {
		return count, fmt.Errorf("list managed volumes: %w", err)
	}
	for _, volume := range strings.Fields(volumes.Stdout) {
		if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"volume", "rm", volume}}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func DefaultConfig() (Config, error) {
	return Config{Image: "nox-runner:v0", CPU: "2", Memory: "4g", PIDs: 256}, nil
}
