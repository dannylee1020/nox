package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
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
	rawRuntimes := strings.TrimSpace(info.Stdout)
	if rawRuntimes == "" || rawRuntimes == "null" {
		detail := strings.TrimSpace(info.Stderr)
		if detail == "" {
			detail = "docker info returned no runtime data"
		}
		return fmt.Errorf("Docker is unavailable or inaccessible: %s", detail)
	}
	runtimes := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(rawRuntimes), &runtimes); err != nil {
		return fmt.Errorf("Docker returned invalid runtime data: %w", err)
	}
	if _, ok := runtimes["runsc"]; !ok {
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

func (d Docker) CreateBaselineVolume(ctx context.Context, runID string) (string, error) {
	return d.createManagedVolume(ctx, "nox-"+runID+"-baseline", runID, "baseline")
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
		"--user", "0:0",
		"--security-opt", "no-new-privileges:true",
		"--cpus", config.CPU,
		"--memory", config.Memory,
		"--pids-limit", strconv.Itoa(config.PIDs),
		"--network", network,
		"--workdir", "/workspace",
		"--mount", "type=volume,src=" + config.WorkspaceVolume + ",dst=/workspace",
		"--tmpfs", "/tmp:rw,exec,nosuid,size=256m",
		"--tmpfs", "/var/tmp:rw,exec,nosuid,size=512m",
	}
	if config.CodexHomeVolume != "" {
		args = append(args, "--mount", "type=volume,src="+config.CodexHomeVolume+",dst=/home/nox/.codex,volume-nocopy")
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

func (d Docker) CheckWorkspaceUnchanged(ctx context.Context, candidateVolume, baselineVolume, image, runID string) (string, error) {
	if candidateVolume == "" || baselineVolume == "" || image == "" || runID == "" {
		return "", fmt.Errorf("candidate volume, baseline volume, image, and run id are required")
	}
	helper, err := d.createIntegrityHelper(ctx, candidateVolume, baselineVolume, image, runID)
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Remove(context.Background(), helper) }()
	if err := d.Start(ctx, helper); err != nil {
		return "", fmt.Errorf("start integrity helper: %w", err)
	}
	var stdout, stderr bytes.Buffer
	result, execErr := d.Exec(ctx, helper, []string{"node", "-e", integrityCheckScript}, nil, &stdout, &stderr)
	if execErr != nil || result.ExitCode != 0 {
		detail := strings.TrimSpace(strings.TrimSpace(stderr.String()) + " " + strings.TrimSpace(stdout.String()))
		if detail == "" {
			detail = fmt.Sprintf("integrity helper exited with code %d", result.ExitCode)
		}
		return detail, fmt.Errorf("tracked source integrity check failed: %s", detail)
	}
	return "", nil
}

func (d Docker) createIntegrityHelper(ctx context.Context, candidateVolume, baselineVolume, image, runID string) (Container, error) {
	name := "nox-" + runID + "-integrity"
	result, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{
		"create",
		"--runtime", "runsc",
		"--name", name,
		"--label", "io.nox.managed=true",
		"--label", "io.nox.run-id=" + runID,
		"--label", "io.nox.kind=integrity-helper",
		"--user", "0:0",
		"--security-opt", "no-new-privileges:true",
		"--network", "none",
		"--mount", "type=volume,src=" + candidateVolume + ",dst=/candidate,readonly",
		"--mount", "type=volume,src=" + baselineVolume + ",dst=/baseline,readonly",
		image, "sleep", "infinity",
	}})
	if err != nil {
		return Container{}, fmt.Errorf("create integrity helper: %w", err)
	}
	id := strings.TrimSpace(result.Stdout)
	if id == "" {
		return Container{}, fmt.Errorf("Docker returned an empty integrity helper id")
	}
	return Container{ID: id}, nil
}

const integrityCheckScript = `const fs = require("fs");
const path = require("path");
const child = require("child_process");
const listed = child.spawnSync("git", ["-c", "safe.directory=/baseline", "-C", "/baseline", "ls-files", "-z"], {encoding: "buffer"});
if (listed.status !== 0) {
  process.stderr.write("trusted baseline file list failed\n" + (listed.stderr || ""));
  process.exit(2);
}
const files = listed.stdout.toString().split("\0").filter(Boolean);
const changed = [];
const safePath = (root, name) => {
  if (path.isAbsolute(name) || name.split("/").includes("..")) return null;
  return path.join(root, name);
};
const kind = (entry) => entry.isSymbolicLink() ? "link" : entry.isFile() ? "file" : entry.isDirectory() ? "directory" : "other";
for (const name of files) {
  const baseline = safePath("/baseline", name);
  const candidate = safePath("/candidate", name);
  if (!baseline || !candidate) { changed.push(name); continue; }
  let expected, actual;
  try { expected = fs.lstatSync(baseline); } catch (_) { changed.push(name); continue; }
  try { actual = fs.lstatSync(candidate); } catch (_) { changed.push(name); continue; }
  if (kind(expected) !== kind(actual)) { changed.push(name); continue; }
  if (kind(expected) === "link") {
    if (fs.readlinkSync(baseline) !== fs.readlinkSync(candidate)) changed.push(name);
    continue;
  }
  if (kind(expected) !== "file" || ((expected.mode & 0o111) !== (actual.mode & 0o111)) || !fs.readFileSync(baseline).equals(fs.readFileSync(candidate))) changed.push(name);
}
if (changed.length) {
  for (const name of changed.slice(0, 50)) process.stderr.write("changed tracked path: " + name + "\n");
  if (changed.length > 50) process.stderr.write("and " + (changed.length - 50) + " more tracked paths\n");
  process.exit(1);
}
process.stdout.write("tracked source integrity passed\n");`

func (d Docker) Start(ctx context.Context, container Container) error {
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"start", container.ID}}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

func (d Docker) PrepareWorkspace(ctx context.Context, container Container) error {
	_, err := d.Exec(ctx, container, []string{"git", "config", "--global", "--add", "safe.directory", "/workspace"}, nil, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("prepare workspace Git trust: %w", err)
	}
	return nil
}

func (d Docker) HasRepositorySetup(ctx context.Context, container Container) (bool, error) {
	result, err := d.Exec(ctx, container, []string{"test", "-f", "/workspace/.nox/setup.sh"}, nil, io.Discard, io.Discard)
	if err == nil && result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect repository setup: %w", err)
	}
	return false, fmt.Errorf("inspect repository setup exited with code %d", result.ExitCode)
}

func (d Docker) RunRepositorySetup(ctx context.Context, container Container, stdout, stderr io.Writer) (execx.Result, error) {
	const command = `set +e
before="$(git status --porcelain --untracked-files=all)"
before_head="$(git rev-parse HEAD)"
sh /workspace/.nox/setup.sh
setup_status=$?
after="$(git status --porcelain --untracked-files=all)"
after_head="$(git rev-parse HEAD)"
if [ "$before" != "$after" ] || [ "$before_head" != "$after_head" ]; then
  printf '%s\n' 'repository setup changed tracked or non-ignored workspace files' >&2
  git status --short --untracked-files=all >&2
  exit 86
fi
exit "$setup_status"`
	return d.Exec(ctx, container, []string{"sh", "-lc", command}, nil, stdout, stderr)
}

func (d Docker) PrepareCodexHome(ctx context.Context, container Container, source string) error {
	if strings.TrimSpace(source) == "" {
		return fmt.Errorf("Codex home source is required")
	}
	if _, err := d.Runner.Run(ctx, execx.Command{Name: "docker", Args: []string{"cp", source + string(os.PathSeparator) + ".", container.ID + ":/home/nox/.codex"}}); err != nil {
		return fmt.Errorf("copy Codex home: %w", err)
	}
	if _, err := d.Exec(ctx, container, []string{"chown", "-R", "1000:1000", "/home/nox/.codex"}, nil, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("set Codex home ownership: %w", err)
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
