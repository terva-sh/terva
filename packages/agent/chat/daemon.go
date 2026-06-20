package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PIDPath returns the location of a service's bot pid file. The
// telegram service keeps its pre-registry filenames (bot.pid,
// logs/bot.log) so existing deployments keep working; every other
// service gets name-prefixed files.
func PIDPath(tervaHome, service string) string {
	if service == "telegram" {
		return filepath.Join(tervaHome, "bot.pid")
	}
	return filepath.Join(tervaHome, service+"-bot.pid")
}

// LogPath returns the location of a service's bot log file
// (stdout+stderr from a detached `terva bot start`).
func LogPath(tervaHome, service string) string {
	if service == "telegram" {
		return filepath.Join(tervaHome, "logs", "bot.log")
	}
	return filepath.Join(tervaHome, "logs", service+"-bot.log")
}

// WritePID persists pid to the service's pid file. Overwrites any
// existing file.
func WritePID(tervaHome, service string, pid int) error {
	p := PIDPath(tervaHome, service)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

// ReadPID returns the pid stored in the service's pid file, or 0 if
// the file doesn't exist. Returns an error for any other read/parse
// failure.
func ReadPID(tervaHome, service string) (int, error) {
	b, err := os.ReadFile(PIDPath(tervaHome, service))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

// RemovePID deletes the service's pid file if it exists.
func RemovePID(tervaHome, service string) error {
	err := os.Remove(PIDPath(tervaHome, service))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// IsRunning returns (pid, true) if a live process with the recorded
// pid exists, or (pid, false) if the pid file points to a dead process.
// Stale pid files are left in place; the caller may remove them.
func IsRunning(tervaHome, service string) (int, bool, error) {
	pid, err := ReadPID(tervaHome, service)
	if err != nil {
		return 0, false, err
	}
	if pid <= 0 {
		return 0, false, nil
	}
	alive, err := processAlive(pid)
	if err != nil {
		return pid, false, nil
	}
	return pid, alive, nil
}

// StopProcess asks pid to exit and waits up to graceful for it to stop,
// then escalates to a forced kill. Returns nil if the process is gone.
// The platform-specific signalling lives in stopProcess (unix sends
// SIGTERM; Windows uses os.Interrupt).
func StopProcess(pid int, graceful time.Duration) error {
	return stopProcess(pid, graceful)
}
