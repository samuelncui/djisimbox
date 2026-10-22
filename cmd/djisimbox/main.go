// Command djisimbox forwards stored incoming SMS to explicitly configured Telegram chats.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/modem"
	"github.com/samuelncui/djisimbox/internal/queue"
)

var version = "dev"

func main() {
	// Keep operational logs structured and free of SMS bodies and credentials.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := execute(os.Args[1:], logger); err != nil {
		logger.Error("djisimbox stopped", "error", err)
		os.Exit(1)
	}
}

func execute(args []string, logger *slog.Logger) error {
	// Parse one explicit operation rather than starting a daemon on an accidental invocation.
	if len(args) == 0 {
		return errors.New("usage: djisimbox {run|check|status|version} [-config /etc/djisimbox/config.json]")
	}
	command := args[0]
	if command == "version" {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}
	if command != "run" && command != "check" && command != "status" {
		return errors.New("unknown command; choose run, check, status, or version")
	}
	flags := flag.NewFlagSet("djisimbox "+command, flag.ContinueOnError)
	path := flags.String("config", "/etc/djisimbox/config.json", "configuration file")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("parse command options: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if command == "status" {
		return printStatus(context.Background(), cfg.RuntimeDir)
	}

	// Check the host protection before connecting to or enabling any modem.
	if err := modem.CheckNetwork(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	inbox, err := modem.Open(logger)
	if err != nil {
		return err
	}
	defer inbox.Close()
	if command == "check" {
		if err := checkRuntimeFilesystem(cfg.RuntimeDir); err != nil {
			return err
		}
		devices, err := inbox.Devices(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			ConfiguredSIMs int            `json:"configured_sims"`
			Modems         []modem.Device `json:"modems"`
		}{ConfiguredSIMs: len(cfg.Routes), Modems: devices})
	}

	// Runtime metadata belongs on tmpfs; even accidental disk-based configuration is rejected.
	if err := prepareRuntime(cfg.RuntimeDir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.RuntimeDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open service lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another djisimbox instance owns this runtime directory")
	}
	pending, err := queue.New(cfg.MaxPending, cfg.MaxBytes)
	if err != nil {
		return err
	}
	logger.Info("djisimbox started", "version", version, "storage", "memory", "configured_sims", len(cfg.Routes))
	return runWithStatus(ctx, cfg, pending, inbox, logger)
}
