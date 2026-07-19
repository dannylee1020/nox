package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nox-dev/nox/internal/remote"
	"github.com/nox-dev/nox/internal/store"
)

func watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateRoot := fs.String("state-root", "", "run state directory")
	remoteMode := fs.Bool("remote", false, "watch a remote run using NOX_REMOTE_URL")
	interval := fs.Duration("interval", 500*time.Millisecond, "metadata and log polling interval")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: nox watch [--remote|--state-root <dir>] <run-id>")
	}
	if *remoteMode && *stateRoot != "" {
		return errors.New("use either --remote or --state-root, not both")
	}
	if *interval <= 0 {
		return errors.New("watch interval must be positive")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *remoteMode {
		client, err := remote.NewClient(os.Getenv("NOX_REMOTE_URL"), os.Getenv("NOX_API_TOKEN"), nil)
		if err != nil {
			return err
		}
		return watchRemoteRun(ctx, client, fs.Arg(0), *interval, os.Stdout, os.Stderr)
	}

	var st store.Store
	if *stateRoot == "" {
		var err error
		st, err = defaultStore()
		if err != nil {
			return err
		}
	} else {
		st = store.New(*stateRoot)
	}
	return watchRun(ctx, st, fs.Arg(0), *interval, os.Stdout, os.Stderr)
}

func watchRemoteRun(ctx context.Context, client *remote.Client, id string, interval time.Duration, output, status io.Writer) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("run id is required")
	}
	current, err := client.Status(ctx, id)
	if err != nil {
		return err
	}
	lastState := ""
	final, err := client.Wait(ctx, current, interval, func(next remote.RunStatus) {
		if next.State == lastState {
			return
		}
		lastState = next.State
		_, _ = fmt.Fprintf(status, "run %s: %s\n", next.RunID, next.State)
	})
	if errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(status, "stopped watching remote run %s; the run continues\n", id)
		return nil
	}
	if err != nil {
		return err
	}
	return writeRemoteResult(output, final)
}

func writeRemoteResult(output io.Writer, status remote.RunStatus) error {
	if status.State == remote.StateCompleted {
		if status.PullRequestURL == "" {
			return errors.New("remote run completed without a pull request URL")
		}
		_, err := fmt.Fprintf(output, "pull request: %s\n", status.PullRequestURL)
		return err
	}
	if status.State == remote.StateNoChanges {
		_, err := fmt.Fprintf(output, "remote run %s made no changes\n", status.RunID)
		return err
	}
	return submitStatusError(status)
}

func watchRun(ctx context.Context, st store.Store, id string, interval time.Duration, output, status io.Writer) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("run id is required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	offsets := make(map[string]int64)
	headers := make(map[string]bool)
	var lastState store.State
	for {
		metadata, err := st.ReadMetadata(id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("run %s was removed while watching", id)
			}
			return err
		}
		if metadata.State != lastState {
			if _, err := fmt.Fprintf(status, "run %s: %s\n", id, metadata.State); err != nil {
				return err
			}
			lastState = metadata.State
		}
		for _, name := range []string{"agent.log", "validation.log"} {
			if err := tailLog(st.RunDir(id)+string(os.PathSeparator)+name, name, offsets, headers, output); err != nil {
				return err
			}
		}
		if metadata.State == store.StateCompleted || metadata.State == store.StateFailed || metadata.State == store.StateCancelled {
			if metadata.Error != "" {
				_, _ = fmt.Fprintf(status, "error: %s\n", metadata.Error)
			}
			if metadata.State == store.StateCompleted {
				return nil
			}
			if metadata.Error == "" {
				return fmt.Errorf("run %s ended %s", id, metadata.State)
			}
			return fmt.Errorf("run %s ended %s: %s", id, metadata.State, metadata.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func tailLog(path, name string, offsets map[string]int64, headers map[string]bool, output io.Writer) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	offset := offsets[name]
	if offset > int64(len(data)) {
		offset = 0
		headers[name] = false
	}
	if offset == int64(len(data)) {
		return nil
	}
	if !headers[name] {
		if _, err := fmt.Fprintf(output, "==> %s <==\n", name); err != nil {
			return err
		}
		headers[name] = true
	}
	if _, err := output.Write(data[offset:]); err != nil {
		return err
	}
	offsets[name] = int64(len(data))
	return nil
}
