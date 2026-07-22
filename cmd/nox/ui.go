package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	dashboard "github.com/nox-dev/nox/internal/ui"
)

func uiCommand(args []string) error {
	defaultRunsRoot, err := uiRunsRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", getenv("NOX_UI_LISTEN_ADDR", "127.0.0.1:8081"), "loopback HTTP listen address")
	runsRoot := fs.String("runs-root", getenv("NOX_UI_RUNS_ROOT", defaultRunsRoot), "run evidence directory")
	remoteStatusRoot := fs.String("remote-status-root", os.Getenv("NOX_UI_REMOTE_STATUS_ROOT"), "remote job status directory")
	recentValue := fs.String("recent", getenv("NOX_UI_RECENT_RUNS", "20"), "number of recent terminal runs")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: nox ui [--listen 127.0.0.1:8081] [--runs-root <dir>] [--remote-status-root <dir>] [--recent 20]")
	}
	if err := validateUILoopback(*listen); err != nil {
		return err
	}
	recent, err := strconv.Atoi(*recentValue)
	if err != nil || recent <= 0 {
		return fmt.Errorf("--recent/NOX_UI_RECENT_RUNS must be a positive integer, got %q", *recentValue)
	}
	service, err := dashboard.New(dashboard.Config{
		RunsRoot: *runsRoot, RemoteStatusRoot: *remoteStatusRoot, Recent: recent,
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	httpServer := &http.Server{
		Handler: service.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
	}()
	fmt.Printf("nox ui listening on http://%s\n", listener.Addr().String())
	if err := httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func uiRunsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nox", "runs"), nil
}

func validateUILoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid UI listen address %q: %w", address, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("UI listen address must use loopback, got %q", address)
	}
	return nil
}
