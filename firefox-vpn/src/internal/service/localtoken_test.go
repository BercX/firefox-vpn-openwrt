package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalTokenBypassesHostCheckAndLogin(t *testing.T) {
	d := newTestDaemon(t)
	d.localToken = "local-token-for-test"
	d.mu.Lock()
	d.cfg.Dashboard.Username = "admin"
	d.cfg.Dashboard.PasswordHash = "unused"
	d.mu.Unlock()
	handler := newDashboardServer(d).handler()

	send := func(token, host string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.Host = host
		if token != "" {
			req.Header.Set(localTokenHeader, token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send("", "127.0.0.1:10820"); code != http.StatusUnauthorized {
		t.Fatalf("without token: got %d, want 401", code)
	}
	if code := send("wrong", "127.0.0.1:10820"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", code)
	}
	if code := send(d.localToken, "rebind.example:10820"); code != http.StatusOK {
		t.Fatalf("local token: got %d, want 200", code)
	}

	// Without a login, a wrong token must not lift the Host check.
	d.mu.Lock()
	d.cfg.Dashboard.Username = ""
	d.mu.Unlock()
	if code := send("wrong", "rebind.example:10820"); code != http.StatusForbidden {
		t.Fatalf("wrong token with a host name: got %d, want 403", code)
	}
}

func TestDaemonShutdownStopsDashboardAndPublishesToken(t *testing.T) {
	d := newTestDaemon(t)
	d.mu.Lock()
	d.cfg.Dashboard.Listen = "127.0.0.1:0"
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- d.Run() }()
	t.Cleanup(d.Shutdown)

	deadline := time.Now().Add(5 * time.Second)
	var token string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(d.paths.LocalToken); err == nil {
			token = string(data)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(token) != 64 || token != d.localToken {
		t.Fatal("published token does not match the daemon's")
	}
	addr := d.dashboard.listener.Addr().String()

	d.Shutdown()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after Shutdown")
	}
	if _, err := http.Get("http://" + addr + "/api/status"); err == nil {
		t.Fatal("dashboard still answers after Shutdown")
	}
	d.Shutdown() // a second call must not block or panic
}

// LuCI needs the local API: refuse to run unreachable.
func TestDaemonRefusesDisabledAPI(t *testing.T) {
	d := newTestDaemon(t)
	d.mu.Lock()
	d.cfg.Dashboard.Enabled = false
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- d.Run() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "dashboard.enabled=false") {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		d.Shutdown()
		t.Fatal("run must fail at once")
	}
	d.Shutdown() // returns at once after a failed Run
}
