package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/samuelncui/djisimbox/internal/app"
	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/queue"
)

const tmpfsMagic = 0x01021994

func prepareRuntime(path string) error {
	// Validate placement before creating runtime metadata.
	if err := checkRuntimeFilesystem(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create memory runtime directory: %w", err)
	}
	return nil
}

func checkRuntimeFilesystem(path string) error {
	// Verify the backing filesystem before creating even an empty directory.
	parent := path
	for {
		var stat syscall.Statfs_t
		err := syscall.Statfs(parent, &stat)
		if errors.Is(err, os.ErrNotExist) {
			parent = filepath.Dir(parent)
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect runtime filesystem: %w", err)
		}
		if stat.Type != tmpfsMagic {
			return errors.New("runtime_dir must be on tmpfs; use /run/djisimbox")
		}
		break
	}
	return nil
}

func statusListener(dir string, pending *queue.Queue) (*http.Server, net.Listener, error) {
	// A private local socket exposes counts only; no SMS text, SIM identities, or tokens leave the process.
	path := filepath.Join(dir, "status.sock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("status socket path contains a regular file")
		}
		if err := os.Remove(path); err != nil {
			return nil, nil, fmt.Errorf("remove stale runtime socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("inspect runtime socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, fmt.Errorf("listen for local status: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, nil, fmt.Errorf("protect runtime socket: %w", err)
	}

	// Bound a stalled local client and expose one read-only operation.
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
		IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/status" {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Storage string      `json:"storage"`
				Queue   queue.Stats `json:"queue"`
			}{Storage: "memory", Queue: pending.Stats()})
		})}
	return server, listener, nil
}

func runWithStatus(ctx context.Context, cfg *config.Config, pending *queue.Queue,
	inbox app.Inbox, logger *slog.Logger) error {
	// Join both the application and local status server before returning from shutdown.
	server, listener, err := statusListener(cfg.RuntimeDir, pending)
	if err != nil {
		return err
	}
	defer listener.Close()
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		results <- err
	}()
	go func() { results <- app.Run(ctx, cfg, pending, inbox, logger) }()
	first := <-results
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
	}
	second := <-results
	if parent.Err() != nil {
		return nil
	}
	return errors.Join(first, second)
}

func printStatus(ctx context.Context, dir string) error {
	// Route this client only to the protected Unix socket, never to an Internet endpoint.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", filepath.Join(dir, "status.sock"))
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/status", nil)
	if err != nil {
		return errors.New("create local status request failed")
	}
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return errors.New("service status unavailable; check systemctl status djisimbox")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("service status request rejected")
	}
	if _, err := io.Copy(os.Stdout, io.LimitReader(response.Body, 64*1024)); err != nil {
		return fmt.Errorf("read local status: %w", err)
	}
	return nil
}
