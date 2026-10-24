// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"firefox-vpn/internal/config"
)

// rpcd executable plugins receive JSON on stdin, not credentials in argv.
var methods = map[string]map[string]any{
	"read":  {"action": "", "id": ""},
	"write": {"action": "", "id": "", "data": ""},
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type rpcInput struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Data   string `json:"data"`
}

type bridge struct {
	baseURL   string
	tokenPath string
	initPath  string
	client    *http.Client
}

func newBridge(dir string) bridge {
	return bridge{
		baseURL:   "http://" + apiAddress,
		tokenPath: filepath.Join(dir, "local-api.token"),
		initPath:  "/etc/init.d/firefox-vpn",
		client: &http.Client{
			Timeout:       20 * time.Second,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func runRPC(args []string, in io.Reader, out io.Writer, b bridge) int {
	enc := json.NewEncoder(out)
	if len(args) == 1 && args[0] == "list" {
		_ = enc.Encode(methods)
		return 0
	}
	if len(args) != 2 || args[0] != "call" || methods[args[1]] == nil {
		return 2
	}
	var input rpcInput
	// rpcd may leave stdin open until the child exits. Decode one object,
	// rather than waiting for EOF. The request is bounded before decoding.
	err := json.NewDecoder(io.LimitReader(in, 64*1024)).Decode(&input)
	var result any
	if err == nil {
		result, err = b.call(args[1], input)
	}
	if err != nil {
		_ = enc.Encode(map[string]any{"ok": false, "error": err.Error()})
	} else {
		_ = enc.Encode(map[string]any{"ok": true, "data": result})
	}
	return 0
}

func (b bridge) request(method, path string, payload any) (json.RawMessage, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	token, err := os.ReadFile(b.tokenPath)
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		return nil, errors.New("Firefox VPN is not ready. Start the service and try again")
	}
	req, err := http.NewRequest(method, b.baseURL+"/api/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Local-Token", strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, errors.New("Firefox VPN did not respond. Check the service and system log")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || !json.Valid(data) {
		return nil, errors.New("Invalid or oversized response from Firefox VPN")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error == "" {
			problem.Error = fmt.Sprintf("Firefox VPN returned HTTP %d", resp.StatusCode)
		}
		return nil, errors.New(problem.Error)
	}
	return data, nil
}

func decodeData(raw string, dst any) error {
	if raw == "" {
		raw = "{}"
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errors.New("Invalid request data")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("Invalid request data")
	}
	return nil
}

func (b bridge) call(method string, in rpcInput) (any, error) {
	if method == "read" {
		switch in.Action {
		case "service":
			err := b.serviceCommand("enabled")
			var exit *exec.ExitError
			if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
				return nil, errors.New("Could not read Firefox VPN startup state")
			}
			return map[string]bool{"enabled": err == nil}, nil
		case "status", "accounts", "locations":
			return b.request("GET", in.Action, nil)
		case "logs":
			data, err := b.request("GET", "logs", nil)
			if err != nil {
				return nil, err
			}
			var view struct {
				Entries []json.RawMessage `json:"entries"`
			}
			if err := json.Unmarshal(data, &view); err != nil {
				return nil, err
			}
			// Keep the ubus reply small even after the core's log ring fills.
			if len(view.Entries) > 100 {
				view.Entries = view.Entries[len(view.Entries)-100:]
			}
			return view, nil
		case "signin":
			if validID.MatchString(in.ID) {
				return b.request("GET", "signin/"+in.ID, nil)
			}
		case "settings":
			cfg, err := b.settings()
			if err != nil {
				return nil, err
			}
			return visibleSettings(cfg), nil
		}
		return nil, errors.New("Unknown read action or invalid ID")
	}
	if method != "write" {
		return nil, errors.New("Unknown method")
	}
	switch in.Action {
	case "test":
		return b.request("POST", "test?background=1", struct{}{})
	case "connection_links":
		cfg, err := b.settings()
		if err != nil {
			return nil, err
		}
		return connectionLinks(cfg), nil
	case "stop_service", "restart_service":
		action := strings.TrimSuffix(in.Action, "_service")
		if err := b.serviceCommand(action); err != nil {
			return nil, fmt.Errorf("Could not %s Firefox VPN; check the system log", action)
		}
		return map[string]bool{"success": true}, nil
	case "service_enable":
		var v struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeData(in.Data, &v); err != nil || v.Enabled == nil {
			return nil, errors.New("Service enabled must be true or false")
		}
		actions := []string{"disable", "stop"}
		if *v.Enabled {
			actions = []string{"enable", "start"}
		}
		for _, action := range actions {
			if err := b.serviceCommand(action); err != nil {
				return nil, fmt.Errorf("Could not %s Firefox VPN; check the system log", action)
			}
		}
		return map[string]bool{"enabled": *v.Enabled}, nil
	case "signin_start":
		var v struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeData(in.Data, &v); err != nil {
			return nil, err
		}
		if len(v.Email) > 320 || len(v.Password) > 4096 || v.Email == "" || v.Password == "" {
			return nil, errors.New("Email and password are required (maximum 320 and 4096 bytes)")
		}
		return b.request("POST", "signin", v)
	case "signin_answer", "signin_cancel", "account_remove", "account_activate", "account_enable", "account_refresh":
		if !validID.MatchString(in.ID) {
			return nil, errors.New("Invalid ID")
		}
		switch in.Action {
		case "signin_answer":
			var v struct {
				Value string `json:"value"`
			}
			if err := decodeData(in.Data, &v); err != nil {
				return nil, err
			}
			if len(v.Value) > 1024 {
				return nil, errors.New("Answer is too long")
			}
			return b.request("POST", "signin/"+in.ID, v)
		case "signin_cancel":
			return b.request("DELETE", "signin/"+in.ID, nil)
		case "account_remove":
			return b.request("DELETE", "accounts/"+in.ID, nil)
		case "account_enable":
			var v struct {
				Enabled bool `json:"enabled"`
			}
			if err := decodeData(in.Data, &v); err != nil {
				return nil, err
			}
			return b.request("POST", "accounts/"+in.ID+"/enabled", v)
		case "account_activate":
			return b.request("POST", "accounts/"+in.ID+"/activate", struct{}{})
		case "account_refresh":
			return b.request("POST", "accounts/"+in.ID+"/refresh", struct{}{})
		}
	case "settings":
		var settings settingsInput
		if err := decodeData(in.Data, &settings); err != nil {
			return nil, err
		}
		cfg, err := b.settings()
		if err != nil {
			return nil, err
		}
		if err := applySettings(&cfg, settings); err != nil {
			return nil, err
		}
		return b.request("PUT", "settings", map[string]any{"config": cfg})
	}
	return nil, errors.New("Unknown write action")
}

func (b bridge) serviceCommand(action string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, b.initPath, action).Run()
}

// Authenticated URIs are only returned by the write-authorized RPC method.
func connectionLinks(c config.Config) map[string]string {
	u := url.URL{Host: c.Proxy.Listen}
	if c.Proxy.Username != "" {
		u.User = url.UserPassword(c.Proxy.Username, c.Proxy.Password)
	}
	links := make(map[string]string)
	for _, scheme := range []string{"socks5", "http"} {
		u.Scheme = scheme
		links[scheme] = u.String()
	}
	return links
}

type settingsInput struct {
	Listen         string   `json:"listen"`
	Username       string   `json:"username"`
	Password       string   `json:"password"`
	Country        string   `json:"country"`
	Server         string   `json:"server"`
	Transport      string   `json:"transport"`
	UDPMode        string   `json:"udp_mode"`
	MaxConnections int      `json:"max_connections"`
	AllowLocked    bool     `json:"allow_locked"`
	DNSServers     []string `json:"dns_servers"`
	DNSFallback    bool     `json:"dns_fallback"`
	LogLevel       string   `json:"log_level"`
}

func visibleSettings(c config.Config) settingsInput {
	return settingsInput{
		Listen: c.Proxy.Listen, Username: c.Proxy.Username,
		Country: c.Proxy.Country, Server: c.Proxy.Server, Transport: c.Proxy.Transport,
		UDPMode:        c.Proxy.UDPMode,
		MaxConnections: c.Proxy.MaxConnections, AllowLocked: c.Proxy.AllowLocked,
		DNSServers: c.DNS.Servers, DNSFallback: c.DNS.FallbackToSystem,
		LogLevel: c.Logging.LogLevel(),
	}
}

func (b bridge) settings() (config.Config, error) {
	data, err := b.request("GET", "settings", nil)
	if err != nil {
		return config.Config{}, err
	}
	var view struct {
		Config config.Config `json:"config"`
	}
	err = json.Unmarshal(data, &view)
	return view.Config, err
}

func applySettings(c *config.Config, in settingsInput) error {
	_, port, err := net.SplitHostPort(in.Listen)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 1 || n > 65535 {
		return errors.New("Listen address must include a port between 1 and 65535")
	}
	if in.MaxConnections < 1 || in.MaxConnections > 4096 {
		return errors.New("Maximum connections must be between 1 and 4096")
	}
	c.Proxy.Listen, c.Proxy.Username = in.Listen, in.Username
	// Empty password keeps the current one; clearing the username disables auth.
	if in.Password != "" || in.Username == "" {
		c.Proxy.Password = in.Password
	}
	c.Proxy.Country, c.Proxy.Server, c.Proxy.Transport = in.Country, in.Server, in.Transport
	if in.UDPMode != "" {
		c.Proxy.UDPMode = in.UDPMode
	}
	c.Proxy.MaxConnections, c.Proxy.AllowLocked = in.MaxConnections, in.AllowLocked
	c.DNS.Servers, c.DNS.FallbackToSystem = in.DNSServers, in.DNSFallback
	if in.LogLevel != "" {
		c.Logging.Level = in.LogLevel
		c.Logging.Verbose = in.LogLevel == "debug"
	}
	if !c.Dashboard.Enabled || c.Dashboard.Listen != apiAddress {
		return errors.New("Invalid local API configuration")
	}
	// GET /settings redacts the dashboard hash. The core restores it on PUT;
	// validate the editable settings without treating that redaction as invalid.
	validation := *c
	validation.Dashboard.Username, validation.Dashboard.PasswordHash = "", ""
	return validation.Validate()
}
