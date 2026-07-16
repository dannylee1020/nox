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
	Image       string
	Workspace   string
	RunID       string
	Network     string
	CPU         string
	Memory      string
	PIDs        int
	CodexHome   string
	Environment map[string]string
}

type Docker struct {
	Runner execx.StreamRunner
}

type Container struct {
	ID     string
	Config Config
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
		"run", "--rm", "--pull=never", "--runtime", "runsc", "--network", "none", "--pids-limit", "16", image, "true",
	}})
	if err != nil || test.ExitCode != 0 {
		return fmt.Errorf("runsc smoke container failed: %w", err)
	}
	return nil
}

func (d Docker) Create(ctx context.Context, config Config) (Container, error) {
	if config.Image == "" || config.Workspace == "" || config.RunID == "" {
		return Container{}, fmt.Errorf("image, workspace, and run id are required")
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
		"--mount", "type=bind,src=" + config.Workspace + ",dst=/workspace",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
		"--tmpfs", "/var/tmp:rw,exec,nosuid,size=512m",
	}
	if config.CodexHome != "" {
		args = append(args, "--mount", "type=bind,src="+config.CodexHome+",dst=/home/nox/.codex,readonly")
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

func (d Docker) Start(ctx context.Context, container Container) error {
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"start", container.ID}}); err != nil {
		return fmt.Errorf("start container: %w", err)
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
	return count, nil
}

func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	return Config{Image: "nox-runner:v0", CPU: "2", Memory: "4g", PIDs: 256, CodexHome: home + "/.codex"}, nil
}
