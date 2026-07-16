package execx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Command is an argv-based process invocation. No shell is used by Runner.
type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
	In   io.Reader
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

type CommandRunner interface {
	Run(context.Context, Command) (Result, error)
}

type StreamRunner interface {
	CommandRunner
	Stream(context.Context, Command, io.Writer, io.Writer) (Result, error)
}

type Runner struct{}

func (Runner) Run(ctx context.Context, command Command) (Result, error) {
	started := time.Now()
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	cmd.Stdin = command.In
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}
	if err != nil {
		return result, fmt.Errorf("%s: %w", formatCommand(command), err)
	}
	return result, nil
}

func (Runner) Stream(ctx context.Context, command Command, stdout, stderr io.Writer) (Result, error) {
	started := time.Now()
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	cmd.Stdin = command.In
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := Result{Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}
	if err != nil {
		return result, fmt.Errorf("%s: %w", formatCommand(command), err)
	}
	return result, nil
}

func (Runner) Start(ctx context.Context, command Command) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	cmd.Stdin = command.In
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", formatCommand(command), err)
	}
	return cmd, nil
}

func CurrentEnv(extra ...string) []string {
	env := append([]string{}, os.Environ()...)
	return append(env, extra...)
}

func WithEnv(base []string, values map[string]string) []string {
	result := append([]string{}, base...)
	for key, value := range values {
		prefix := key + "="
		found := false
		for i, entry := range result {
			if strings.HasPrefix(entry, prefix) {
				result[i] = prefix + value
				found = true
				break
			}
		}
		if !found {
			result = append(result, prefix+value)
		}
	}
	return result
}

func formatCommand(command Command) string {
	parts := append([]string{command.Name}, command.Args...)
	return strings.Join(parts, " ")
}
