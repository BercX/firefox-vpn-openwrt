package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"firefox-vpn/internal/logx"
	"firefox-vpn/internal/version"
)

func TestStartupLogsPackageVersion(t *testing.T) {
	previous := version.Current
	version.Current = "9.8.7-r6"
	t.Cleanup(func() { version.Current = previous })
	var output bytes.Buffer
	logx.SetOutput(&output)
	t.Cleanup(func() { logx.SetOutput(os.Stderr) })
	dir := t.TempDir()
	// Fail before starting listeners: even failed starts must identify the binary.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	if run(dir) == nil {
		t.Fatal("invalid configuration accepted")
	}
	want := "OpenWrt service starting (firefox-vpn 9.8.7-r6)"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("missing package version in startup log: %s", output.String())
	}
	entries := logx.Logs.Since(0)
	if len(entries) == 0 || entries[len(entries)-1].Message != want {
		t.Fatal("startup version missing from LuCI log buffer")
	}
}
