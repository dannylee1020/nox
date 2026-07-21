package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
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
	if *remoteMode && *stateRoot != "" {
		return errors.New("use either --remote or --state-root, not both")
	}
	if *interval <= 0 {
		return errors.New("watch interval must be positive")
	}
	if len(fs.Args()) > 1 {
		return errors.New("usage: nox watch [--state-root <dir>] [<run-id>]")
	}
	if *remoteMode && len(fs.Args()) != 1 {
		return errors.New("remote watch requires a run ID: nox watch --remote <run-id>")
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
	if len(fs.Args()) == 1 {
		return watchRun(ctx, st, fs.Arg(0), *interval, os.Stdout, os.Stderr)
	}
	return watchActiveRuns(ctx, st, *interval, os.Stdin, os.Stdout, os.Stderr, stdinIsInteractive(os.Stdin), time.Now())
}

type activeRun struct {
	id       string
	metadata store.Metadata
}

func collectActiveRuns(st store.Store, warnings io.Writer) ([]activeRun, error) {
	ids, err := st.ListIDs()
	if err != nil {
		return nil, fmt.Errorf("list local runs: %w", err)
	}
	runs := make([]activeRun, 0, len(ids))
	for _, id := range ids {
		metadata, err := st.ReadMetadata(id)
		if err != nil {
			if _, writeErr := fmt.Fprintf(warnings, "warning: skipping run %s: %v\n", id, err); writeErr != nil {
				return nil, writeErr
			}
			continue
		}
		if isTerminalState(metadata.State) {
			continue
		}
		runs = append(runs, activeRun{id: id, metadata: metadata})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].metadata.StartedAt.After(runs[j].metadata.StartedAt)
	})
	return runs, nil
}

func isTerminalState(state store.State) bool {
	return state == store.StateCompleted || state == store.StateFailed || state == store.StateCancelled
}

func renderActiveRuns(output io.Writer, runs []activeRun, now time.Time) error {
	if _, err := fmt.Fprintf(output, "Active local runs: %d\n", len(runs)); err != nil {
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "#\tRUN ID\tSTATE\tELAPSED\tVALIDATION\tOUTPUT BRANCH"); err != nil {
		return err
	}
	for i, run := range runs {
		branch := run.metadata.OutputBranch
		if branch == "" {
			branch = "-"
		}
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\n",
			i+1,
			run.id,
			run.metadata.State,
			formatElapsed(now, run.metadata.StartedAt),
			validationLabel(run.metadata.State),
			branch,
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

func formatElapsed(now, startedAt time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "0s"
	}
	elapsed := now.Sub(startedAt).Truncate(time.Second)
	if elapsed < time.Second {
		return "0s"
	}
	return elapsed.String()
}

func validationLabel(state store.State) string {
	switch state {
	case store.StateValidating:
		return "running"
	case store.StatePublishing:
		return "passed"
	case store.StateTeardown:
		return "finalizing"
	default:
		return "pending"
	}
}

func selectActiveRun(input io.Reader, output io.Writer, count int) (int, error) {
	scanner := bufio.NewScanner(input)
	for {
		if _, err := fmt.Fprintf(output, "Select a run [1-%d]: ", count); err != nil {
			return 0, err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return 0, fmt.Errorf("select run: %w", err)
			}
			return 0, fmt.Errorf("select run: %w", io.EOF)
		}
		selection, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err == nil && selection >= 1 && selection <= count {
			return selection - 1, nil
		}
		if _, err := fmt.Fprintf(output, "Invalid selection; enter a number from 1 to %d.\n", count); err != nil {
			return 0, err
		}
	}
}

func watchActiveRuns(ctx context.Context, st store.Store, interval time.Duration, input io.Reader, output, status io.Writer, interactive bool, now time.Time) error {
	runs, err := collectActiveRuns(st, status)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		_, err := fmt.Fprintln(output, "No active local runs.")
		return err
	}
	if err := renderActiveRuns(output, runs, now); err != nil {
		return err
	}
	if !interactive {
		if _, err := fmt.Fprintln(output, "\nWatch a run with:"); err != nil {
			return err
		}
		for _, run := range runs {
			if _, err := fmt.Fprintf(output, "  nox watch %s\n", run.id); err != nil {
				return err
			}
		}
		return nil
	}
	selection, err := selectActiveRun(input, output, len(runs))
	if err != nil {
		return err
	}
	return watchRun(ctx, st, runs[selection].id, interval, output, status)
}

func stdinIsInteractive(stdin *os.File) bool {
	info, err := stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
		for _, name := range []string{"setup.log", "agent.log", "validation.log"} {
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
