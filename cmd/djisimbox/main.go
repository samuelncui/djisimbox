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
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/modem"
	"github.com/samuelncui/djisimbox/internal/queue"
	"github.com/samuelncui/djisimbox/internal/setup"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

var version = "dev"

const defaultConfigPath = "/etc/djisimbox/config.yaml"

// errCanceled reports that the user interrupted setup.
var errCanceled = errors.New("canceled")

func main() {
	// Keep operational logs structured and free of SMS bodies and credentials.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := execute(os.Args[1:], logger)
	if err == nil {
		return
	}

	if errors.Is(err, errCanceled) {
		fmt.Fprintln(os.Stderr, "Canceled / 已取消")
		os.Exit(130)
	}

	// The interactive guide talks to people, so its errors are plain text rather than log records.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		fmt.Fprintln(os.Stderr, "Error:", err)
	} else {
		logger.Error("djisimbox stopped", "error", err)
	}
	os.Exit(1)
}

func execute(args []string, logger *slog.Logger) error {
	// Parse one explicit operation rather than starting a daemon on an accidental invocation.
	if len(args) == 0 {
		return errors.New("usage: djisimbox {run|check|status|setup|version} [-config /etc/djisimbox/config.yaml]")
	}
	command := args[0]
	if command == "version" {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}
	if command == "setup" {
		return runSetup(args[1:], logger)
	}
	if command != "run" && command != "check" && command != "status" {
		return errors.New("unknown command; choose run, check, status, setup, or version")
	}
	flags := flag.NewFlagSet("djisimbox "+command, flag.ContinueOnError)
	path := flags.String("config", "/etc/djisimbox/config.yaml", "configuration file")
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

// routeFlags collects every repeated -route option.
type routeFlags []string

func (r *routeFlags) String() string { return strings.Join(*r, " ") }

func (r *routeFlags) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func runSetup(args []string, logger *slog.Logger) error {
	// Parse options; the bot token is never accepted as an argument.
	flags := flag.NewFlagSet("djisimbox setup", flag.ContinueOnError)
	var opts setup.Options
	var routes routeFlags
	flags.StringVar(&opts.ConfigPath, "config", "/etc/djisimbox/config.yaml", "configuration file")
	flags.StringVar(&opts.Language, "language", "", "language, zh-CN or en (skips the language prompt)")
	flags.StringVar(&opts.TokenFile, "token-file", "", "read the bot token from the first line of `PATH`")
	flags.Var(&routes, "route", "`SIM=CHATS`, repeatable; SIM is an ICCID, a phone number or all; CHATS is comma-separated chat IDs or - to remove the route")
	flags.BoolVar(&opts.Yes, "yes", false, "never prompt; use the existing configuration for anything not given and save directly")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse command options: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	opts.Routes = routes
	opts.ServiceConfig = defaultConfigPath
	if os.Geteuid() != 0 {
		return errors.New("run with sudo")
	}

	// Wire the guide to the real terminal, modems, Telegram and systemd.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	err := setup.Run(ctx, opts, setup.Env{
		In:  os.Stdin,
		Out: os.Stdout,
		Devices: func(ctx context.Context) ([]modem.Device, error) {
			inbox, err := modem.Open(logger)
			if err != nil {
				return nil, err
			}
			defer inbox.Close()
			return inbox.Devices(ctx)
		},
		NewBot: func(token string) (setup.Bot, error) {
			bot, err := telegram.New(token, nil)
			if err != nil {
				return nil, err
			}
			return bot, nil
		},
		ServiceInstalled: func(ctx context.Context) bool {
			return exec.CommandContext(ctx, "systemctl", "cat", "djisimbox").Run() == nil
		},
		StartService: func(ctx context.Context) error {
			// Enable for boot, then restart, which also starts a stopped service.
			for _, action := range []string{"enable", "restart"} {
				if err := exec.CommandContext(ctx, "systemctl", action, "djisimbox").Run(); err != nil {
					return fmt.Errorf("systemctl %s: %w", action, err)
				}
			}
			return nil
		},
	})
	if errors.Is(err, context.Canceled) {
		return errCanceled
	}
	return err
}
