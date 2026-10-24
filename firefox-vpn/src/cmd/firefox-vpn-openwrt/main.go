// SPDX-License-Identifier: MIT
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"firefox-vpn/internal/config"
	"firefox-vpn/internal/logx"
	"firefox-vpn/internal/service"
	"firefox-vpn/internal/version"
)

const dataDir = config.DefaultDataDir
const apiAddress = "127.0.0.1:10820"

func main() {
	if filepath.Base(os.Args[0]) == "luci.firefox-vpn" || (len(os.Args) > 1 && (os.Args[1] == "list" || os.Args[1] == "call")) {
		os.Exit(runRPC(os.Args[1:], os.Stdin, os.Stdout, newBridge(dataDir)))
	}
	dir := flag.String("data-dir", dataDir, "Private configuration and account directory")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir string) error {
	// procd collects stderr; do not enable upstream's daily flash log files.
	logx.Info("OpenWrt service starting (firefox-vpn %s)", version.Current)
	paths, err := service.ResolveDataPaths(dir)
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(paths.Config)
	if err != nil {
		return err
	}
	// LuCI reaches this API through rpcd, never through a network listener.
	if !cfg.Dashboard.Enabled || cfg.Dashboard.Listen != apiAddress {
		return fmt.Errorf("dashboard must be enabled on %s for LuCI", apiAddress)
	}
	d, err := service.NewDaemon(paths)
	if err != nil {
		return err
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		<-stop
		d.Shutdown()
	}()
	return d.Run()
}
