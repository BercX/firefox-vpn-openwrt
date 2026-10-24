package logx

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// Verbose enables CONNECT destinations and debug lines; the dashboard
	// can change it at runtime.
	Verbose      atomic.Bool
	logMu        sync.Mutex
	logOutput    io.Writer = os.Stderr
	minimumLevel slog.LevelVar
)

// SetLevel applies a validated configuration level to every log destination.
// Debug retains the existing verbose mode's privacy-sensitive detail.
func SetLevel(level string) {
	var next slog.Level
	switch level {
	case "off":
		next = slog.LevelError + 1
	case "debug":
		next = slog.LevelDebug
	case "warn":
		next = slog.LevelWarn
	case "error":
		next = slog.LevelError
	}
	minimumLevel.Set(next)
	Verbose.Store(next == slog.LevelDebug)
}

// SetOutput changes where log lines are written; the dashboard's log
// view is unaffected.
func SetOutput(w io.Writer) {
	logMu.Lock()
	logOutput = w
	logMu.Unlock()
}

func logEvent(level slog.Level, format string, args ...any) {
	if level < minimumLevel.Level() {
		return
	}
	message := fmt.Sprintf(format, args...)
	now := time.Now()
	logMu.Lock()
	fmt.Fprintf(logOutput, "%s %-5s %s\n", now.Format(time.RFC3339), level, message)
	logMu.Unlock()
	Logs.add(now, level.String(), message)
}

func Debug(format string, args ...any) {
	if !Verbose.Load() {
		return
	}
	logEvent(slog.LevelDebug, format, args...)
}

func Info(format string, args ...any) {
	logEvent(slog.LevelInfo, format, args...)
}

func Warn(format string, args ...any) {
	logEvent(slog.LevelWarn, format, args...)
}

func Error(format string, args ...any) {
	logEvent(slog.LevelError, format, args...)
}

func Target(target string) string {
	if Verbose.Load() {
		return target
	}
	return "<redacted; enable Debug logging>"
}

func Err(err error) string {
	if err == nil {
		return "<nil>"
	}
	if Verbose.Load() {
		return err.Error()
	}
	var r redacter
	if errors.As(err, &r) {
		return r.Redacted()
	}
	return err.Error()
}

// redacter is an error that can describe itself without sensitive details,
// for logs that are not verbose.
type redacter interface {
	error
	Redacted() string
}

func Addr(addr net.Addr) string {
	if addr == nil {
		return "<unknown>"
	}
	return addr.String()
}
