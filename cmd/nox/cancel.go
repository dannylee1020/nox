package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nox-dev/nox/internal/remote"
)

func cancelRun(args []string) error {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	remoteMode := fs.Bool("remote", false, "cancel a remote run using NOX_REMOTE_URL")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !*remoteMode || len(fs.Args()) != 1 {
		return errors.New("usage: nox cancel --remote <run-id>")
	}
	client, err := remote.NewClient(os.Getenv("NOX_REMOTE_URL"), os.Getenv("NOX_API_TOKEN"), nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Cancel(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Printf("cancellation requested for remote run %s\n", fs.Arg(0))
	return nil
}
