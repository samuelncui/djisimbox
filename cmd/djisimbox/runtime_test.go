package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/djisimbox/internal/queue"
)

func TestRuntimeRejectsDiskBeforeCreatingDirectory(t *testing.T) {
	// Inspect the fixture filesystem without creating the requested runtime directory.
	parent := t.TempDir()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(parent, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Type == tmpfsMagic {
		// A tmpfs /tmp is common on development hosts; use a cleaned repository fixture for this rejection case.
		fallback, err := os.MkdirTemp(".", ".djisimbox-runtime-test-")
		if err != nil {
			t.Skip("non-tmpfs fixture unavailable")
		}
		parent = fallback
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		if err := syscall.Statfs(parent, &stat); err != nil {
			t.Fatal(err)
		}
		if stat.Type == tmpfsMagic {
			t.Skip("both temporary and repository filesystems are tmpfs")
		}
	}
	path := filepath.Join(parent, "runtime")
	if err := prepareRuntime(path); err == nil {
		t.Fatal("disk-backed runtime accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected configuration wrote a runtime directory")
	}
}

func TestPrivateStatusSocketExposesCountsOnly(t *testing.T) {
	// A short temporary path fits Unix socket limits on all supported Linux hosts.
	dir, err := os.MkdirTemp("", "djistat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	pending, err := queue.New(10, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pending.Accept(queue.Message{SourceKey: "private-source", SIMID: "private-sim",
		Text: "private-body"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	server, listener, err := statusListener(dir, pending)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("status server: %v", err)
		}
	})
	info, err := os.Stat(filepath.Join(dir, "status.sock"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("status socket is not private")
	}

	// Exercise the actual local HTTP transport without opening a TCP listener.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "status.sock"))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/status", 200},
		{http.MethodPost, "/status", 405},
		{http.MethodGet, "/missing", 404},
	} {
		request, err := http.NewRequest(tc.method, "http://localhost"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != tc.status {
			t.Fatalf("local status %s %s: %d %v", tc.method, tc.path, response.StatusCode, err)
		}
		if strings.Contains(string(body), "private-") {
			t.Fatal("status response exposed private message fields")
		}
		if tc.status == 200 && !strings.Contains(string(body), `"storage":"memory"`) {
			t.Fatal("RAM operation not reported")
		}
	}
}
