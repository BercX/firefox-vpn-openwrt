package service

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"firefox-vpn/internal/logx"
)

func TestServesAPIWithoutUnusedInterfaces(t *testing.T) {
	client := newDashboardClient(t, newTestDaemon(t))
	for _, path := range []string{"/", "/index.html", "/app.js", "/app.css", "/icon.png", "/flags/us.svg"} {
		if rec := client.do(http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("standalone asset %s: got %d, want 404", path, rec.Code)
		}
	}
	for _, path := range []string{
		"/api/open-folder", "/api/app/exit", "/api/system-proxy",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if rec := client.do(method, path, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("removed endpoint %s %s: got %d, want 404 or 405", method, path, rec.Code)
			}
		}
	}
	for _, path := range []string{"/api/status", "/api/accounts", "/api/settings", "/api/locations", "/api/logs", "/api/routing"} {
		if rec := client.do(http.MethodGet, path, nil); rec.Code != http.StatusOK {
			t.Fatalf("LuCI API %s: got %d, want 200: %s", path, rec.Code, rec.Body)
		}
	}
	settings := decodeBody[map[string]any](t, client.do(http.MethodGet, "/api/settings", nil))
	if settings["program_dir"] != programDir() {
		t.Fatal("settings must preserve executable-directory metadata")
	}
}

func TestRestoredAPIsKeepRequestGuards(t *testing.T) {
	d := newTestDaemon(t)
	d.cfg.Dashboard.Username = "admin"
	d.cfg.Dashboard.PasswordHash = "unused"
	c := newDashboardClient(t, d)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/proxy/restart"},
		{http.MethodPost, "/api/power"},
		{http.MethodPost, "/api/accounts/order"},
		{http.MethodGet, "/api/routing"},
		{http.MethodPost, "/api/routing/update"},
		{http.MethodPost, "/api/routing/test"},
		{http.MethodPost, "/api/locations/ping"},
		{http.MethodPost, "/api/location"},
		{http.MethodPost, "/api/dns/test"},
		{http.MethodGet, "/api/logs/stream"},
		{http.MethodPost, "/api/dashboard/reset-login"},
	} {
		if rec := c.do(route.method, route.path, map[string]any{}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", route.path, rec.Code)
		}
		if route.method != http.MethodPost {
			continue
		}
		req := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		req.Host = c.host
		rec := httptest.NewRecorder()
		c.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("non-JSON %s: %d", route.path, rec.Code)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://attacker.example")
		rec = httptest.NewRecorder()
		c.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-origin %s: %d", route.path, rec.Code)
		}
	}
}

func TestRestoredRestartAndAccountOrder(t *testing.T) {
	d := newTestDaemon(t)
	a, b := addTestAccount(t, d, "a"), addTestAccount(t, d, "b")
	c := newDashboardClient(t, d)
	if rec := c.do(http.MethodPost, "/api/accounts/order", map[string][]string{"ids": {b, a}}); rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body)
	}
	if got := d.config().Accounts.Order; got[0].ID != b || got[1].ID != a {
		t.Fatalf("order: %+v", got)
	}
	for _, ids := range [][]string{{b}, {b, b}, {b, "unknown"}} {
		if rec := c.do(http.MethodPost, "/api/accounts/order", map[string][]string{"ids": ids}); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid order %v: %d", ids, rec.Code)
		}
	}
	select {
	case <-d.restart:
	default:
	}
	if rec := c.do(http.MethodPost, "/api/proxy/restart", nil); rec.Code != http.StatusOK {
		t.Fatalf("restart: %d", rec.Code)
	}
	select {
	case <-d.restart:
	default:
		t.Fatal("restart did not reach the supervisor")
	}
}

func TestResetDashboardLogin(t *testing.T) {
	d := newTestDaemon(t)
	d.localToken = "reset-token"
	cfg := d.config()
	cfg.Dashboard.Username = "admin"
	forgotten := "forgotten password"
	if _, err := d.updateSettings(cfg, &forgotten); err != nil {
		t.Fatal(err)
	}
	c := newDashboardClient(t, d)
	rec := c.do(http.MethodPost, "/api/session", map[string]string{"username": "admin", "password": forgotten})
	if rec.Code != http.StatusOK {
		t.Fatalf("old login: %d", rec.Code)
	}
	c.cookie = rec.Result().Cookies()[0]
	// Even an authenticated browser cannot reset the login without the token.
	if rec := c.do(http.MethodPost, "/api/dashboard/reset-login", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("browser reset: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/reset-login", strings.NewReader("{}"))
	req.Host = c.host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localTokenHeader, d.localToken)
	rec = httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local reset: %d %s", rec.Code, rec.Body)
	}
	login := decodeBody[map[string]string](t, rec)
	if !strings.HasPrefix(login["username"], "admin-") || len(login["password"]) != 16 {
		t.Fatal("invalid generated login")
	}
	if rec := c.do(http.MethodGet, "/api/status", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session survived reset: %d", rec.Code)
	}
	if rec := c.do(http.MethodPost, "/api/session", login); rec.Code != http.StatusOK {
		t.Fatalf("new login: %d", rec.Code)
	}
	if rec := c.do(http.MethodPost, "/api/session", map[string]string{"username": "admin", "password": forgotten}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password survived reset: %d", rec.Code)
	}
}

func TestLogStream(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(newDashboardServer(d).handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/logs/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %s %v", resp.Status, resp.Header)
	}
	logx.Info("stream regression marker")
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		if strings.Contains(scan.Text(), "stream regression marker") {
			return
		}
	}
	t.Fatalf("stream did not deliver a log entry: %v", scan.Err())
}
