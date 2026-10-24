package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"firefox-vpn/internal/config"
)

func TestRPCSignInAndAccessBoundary(t *testing.T) {
	var requests []string
	step := "need_email_code"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Local-Token") != "private-test-token" {
			t.Error("missing local authentication")
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/signin":
			var v map[string]string
			json.NewDecoder(r.Body).Decode(&v)
			if v["password"] != "secret \" & ; $()" {
				t.Error("password was modified")
			}
		case "POST /api/signin/test-id":
			var v map[string]string
			json.NewDecoder(r.Body).Decode(&v)
			if v["value"] == "654321" {
				step = "done"
			}
		case "DELETE /api/signin/test-id":
			step = "cancelled"
		case "GET /api/signin/test-id":
		case "POST /api/test":
			if r.URL.RawQuery != "background=1" {
				t.Error("connection test must run in the background")
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"id":"test-id","step":%q}`, step)
	}))
	defer server.Close()
	b := newBridge(t.TempDir())
	b.baseURL, b.client = server.URL, server.Client()
	if err := os.WriteFile(b.tokenPath, []byte("private-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(method string, in rpcInput) string {
		t.Helper()
		input, _ := json.Marshal(in)
		var output bytes.Buffer
		if runRPC([]string{"call", method}, bytes.NewReader(input), &output, b) != 0 {
			t.Fatal("rpcd call failed")
		}
		if strings.Contains(output.String(), "private-test-token") || strings.Contains(output.String(), "secret") {
			t.Fatal("credential exposed")
		}
		return output.String()
	}
	for _, in := range []rpcInput{
		{Action: "signin_start", Data: `{"email":"test@example.org","password":"secret"}`},
		{Action: "stop_service"},
		{Action: "restart_service"},
		{Action: "test"},
		{Action: "connection_links"},
		{Action: "signin", ID: "../settings"},
		{Action: "http://attacker.example"},
	} {
		if !strings.Contains(call("read", in), `"ok":false`) {
			t.Fatal("read ACL allowed mutation or traversal")
		}
	}
	for _, action := range []string{"start_service", "power", "restart"} {
		if !strings.Contains(call("write", rpcInput{Action: action, Data: `{"on":true}`}), "Unknown write action") {
			t.Fatalf("retired action %s is still exposed", action)
		}
	}
	if len(requests) != 0 {
		t.Fatal("rejected action reached upstream")
	}
	credentials, _ := json.Marshal(map[string]string{"email": "test@example.org", "password": "secret \" & ; $()"})
	if !strings.Contains(call("write", rpcInput{Action: "signin_start", Data: string(credentials)}), "need_email_code") {
		t.Fatal("missing code prompt")
	}
	if !strings.Contains(call("write", rpcInput{Action: "signin_answer", ID: "test-id", Data: `{"value":"bad"}`}), "need_email_code") {
		t.Fatal("wrong code cannot be retried")
	}
	if !strings.Contains(call("read", rpcInput{Action: "signin", ID: "test-id"}), "need_email_code") {
		t.Fatal("sign-in state was lost between RPC invocations")
	}
	if !strings.Contains(call("write", rpcInput{Action: "signin_answer", ID: "test-id", Data: `{"value":"654321"}`}), "done") {
		t.Fatal("sign-in did not finish")
	}
	if !strings.Contains(call("write", rpcInput{Action: "signin_cancel", ID: "test-id"}), "cancelled") {
		t.Fatal("cancel failed")
	}
	if !strings.Contains(call("write", rpcInput{Action: "test"}), `"ok":true`) {
		t.Fatal("background test failed")
	}
}

func TestConnectionLinksEscapeCredentials(t *testing.T) {
	c := config.Default()
	c.Proxy.Listen = "[::1]:1088"
	c.Proxy.Username, c.Proxy.Password = "user@name", "p:/?# &%"
	for scheme, raw := range connectionLinks(c) {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		pw, _ := u.User.Password()
		if u.Scheme != scheme || u.Host != c.Proxy.Listen || u.User.Username() != c.Proxy.Username || pw != c.Proxy.Password || u.Fragment != "" || u.RawQuery != "" {
			t.Fatalf("proxy credentials failed to round trip: %s", raw)
		}
	}
	c.Proxy.Username = ""
	for _, raw := range connectionLinks(c) {
		if strings.Contains(raw, "@") {
			t.Fatal("unauthenticated proxy link contains credentials")
		}
	}
}

func TestSettingsKeepSecretsAndValidate(t *testing.T) {
	c := config.Default()
	c.Proxy.Username, c.Proxy.Password = "local", "private-password"
	in := visibleSettings(c)
	encoded, _ := json.Marshal(in)
	if bytes.Contains(encoded, []byte("private-password")) {
		t.Fatal("read settings leaked proxy password")
	}
	if in.LogLevel != "info" {
		t.Fatal("default log level must be Info")
	}
	for _, level := range []string{"debug", "info", "warn", "error", "off"} {
		in.LogLevel = level
		if err := applySettings(&c, in); err != nil || c.Logging.Level != level || c.Logging.Verbose != (level == "debug") || visibleSettings(c).LogLevel != level {
			t.Fatalf("log level %s failed to round trip: %+v %v", level, c.Logging, err)
		}
	}
	in.LogLevel = "trace"
	if applySettings(&c, in) == nil {
		t.Fatal("invalid log level accepted")
	}
	in.LogLevel = ""
	c.Logging = config.Logging{Verbose: true}
	if err := applySettings(&c, in); err != nil || visibleSettings(c).LogLevel != "debug" {
		t.Fatalf("old settings clients must preserve legacy verbose mode: %v", err)
	}
	in.Listen = "192.168.1.1:1088"
	if err := applySettings(&c, in); err != nil {
		t.Fatal(err)
	}
	if c.Proxy.Password != "private-password" {
		t.Fatal("blank password erased existing password")
	}
	in.Listen = "not an address"
	if applySettings(&c, in) == nil {
		t.Fatal("invalid listener accepted")
	}
	in.Listen, in.Username = "127.0.0.1:1088", ""
	if err := applySettings(&c, in); err != nil {
		t.Fatal(err)
	}
	if c.Proxy.Password != "" {
		t.Fatal("disabling proxy auth retained password")
	}
	in.Listen = "127.0.0.1:99999"
	if applySettings(&c, in) == nil {
		t.Fatal("out-of-range port accepted")
	}
	in.Listen = "127.0.0.1:1088"
	c.Dashboard.Username = "admin" // GET /settings redacts its password hash.
	if err := applySettings(&c, in); err != nil {
		t.Fatal(err)
	}
	if c.Dashboard.Username != "admin" {
		t.Fatal("settings change disabled dashboard authentication")
	}
	in.UDPMode = "off"
	if err := applySettings(&c, in); err != nil || c.Proxy.UDPMode != "off" {
		t.Fatalf("UDP mode not applied: %v", err)
	}
	in.UDPMode = "masque"
	if applySettings(&c, in) == nil {
		t.Fatal("accepted removed experimental UDP mode")
	}
	in.UDPMode = "direct"
	if applySettings(&c, in) == nil {
		t.Fatal("accepted unsafe UDP mode")
	}
	var s settingsInput
	if decodeData(`{"dashboard":{"listen":"0.0.0.0:10820"}}`, &s) == nil {
		t.Fatal("unrestricted API configuration accepted")
	}
	if decodeData(`{} {}`, &s) == nil {
		t.Fatal("multiple objects accepted")
	}
}

func TestBridgeDoesNotFollowRedirects(t *testing.T) {
	reached := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer server.Close()
	b := newBridge(t.TempDir())
	b.baseURL = server.URL
	if err := os.WriteFile(filepath.Clean(b.tokenPath), []byte("private-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.request("GET", "status", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if reached {
		t.Fatal("local token forwarded to redirect target")
	}
}

func TestRPCServiceControlsWithoutDaemon(t *testing.T) {
	dir := t.TempDir()
	b := newBridge(dir)
	b.initPath = filepath.Join(dir, "init")
	// A tiny init-script stand-in: no router, daemon, token or shell interpolation from RPC data.
	script := `#!/bin/sh
cd "$(dirname "$0")" || exit 2
echo "$1" >> actions
[ ! -f fail ] || exit 2
case "$1" in
enabled) [ -f enabled ] ;;
enable) touch enabled ;;
disable) rm -f enabled ;;
start|stop|restart) exit 0 ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(b.initPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	readEnabled := func(want bool) {
		t.Helper()
		result, err := b.call("read", rpcInput{Action: "service"})
		if err != nil || result.(map[string]bool)["enabled"] != want {
			t.Fatalf("service state: %v %v", result, err)
		}
	}
	readEnabled(false)
	for _, enabled := range []bool{true, false} {
		os.Remove(filepath.Join(dir, "actions"))
		_, err := b.call("write", rpcInput{Action: "service_enable", Data: fmt.Sprintf(`{"enabled":%t}`, enabled)})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "actions"))
		want := "disable\nstop\n"
		if enabled {
			want = "enable\nstart\n"
		}
		if string(got) != want {
			t.Fatalf("service commands: %q, want %q", got, want)
		}
		readEnabled(enabled)
		for _, action := range []string{"stop", "restart"} {
			os.Remove(filepath.Join(dir, "actions"))
			if _, err := b.call("write", rpcInput{Action: action + "_service"}); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "actions"))
			if string(got) != action+"\n" {
				t.Fatalf("unexpected service command: %q", got)
			}
			readEnabled(enabled) // Runtime actions must preserve boot startup.
		}
	}
	os.Remove(filepath.Join(dir, "actions"))
	for _, raw := range []string{`{}`, `{"enabled":null}`, `{"enabled":"start"}`, `{"enabled":true,"command":"reboot"}`} {
		if _, err := b.call("write", rpcInput{Action: "service_enable", Data: raw}); err == nil {
			t.Fatalf("accepted invalid input %s", raw)
		}
	}
	if _, err := b.call("read", rpcInput{Action: "service_enable", Data: `{"enabled":true}`}); err == nil {
		t.Fatal("read permission changed service")
	}
	for _, action := range []string{"stop_service", "restart_service", "reboot_service", "stop_service;reboot"} {
		if _, err := b.call("read", rpcInput{Action: action}); err == nil {
			t.Fatal("read permission executed a service command")
		}
		if action != "stop_service" && action != "restart_service" {
			if _, err := b.call("write", rpcInput{Action: action}); err == nil {
				t.Fatal("unsupported service command accepted")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "actions")); !os.IsNotExist(err) {
		t.Fatal("invalid input executed a command")
	}
	if err := os.WriteFile(filepath.Join(dir, "fail"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.call("read", rpcInput{Action: "service"}); err == nil {
		t.Fatal("script error reported as disabled")
	}
	if _, err := b.call("write", rpcInput{Action: "service_enable", Data: `{"enabled":true}`}); err == nil {
		t.Fatal("failed enable reported success")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "actions"))
	if strings.Contains(string(got), "start") {
		t.Fatal("start ran after enable failed")
	}
	for _, action := range []string{"stop_service", "restart_service"} {
		if _, err := b.call("write", rpcInput{Action: action}); err == nil {
			t.Fatal("failed service command reported success")
		}
	}
}
