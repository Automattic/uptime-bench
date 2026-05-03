// Package activeguard provides a lightweight local guard for commands that
// mutate provider state or live benchmark rows.
package activeguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// EnvLockPath overrides the default active-run lock path.
	EnvLockPath = "UPTIME_BENCH_ACTIVE_RUN_LOCK"

	// DefaultLockPath is intentionally local to the orchestrator host. It does
	// not call provider APIs, touch the fleet, or require a database.
	DefaultLockPath = "/tmp/uptime-bench-active-run.lock"
)

// LockInfo is the JSON payload written to the active-run lock file.
type LockInfo struct {
	Command   string    `json:"command"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// ActiveRunError reports a lock that indicates another run is active.
type ActiveRunError struct {
	Path string
	Info LockInfo
	Raw  string
}

func (e *ActiveRunError) Error() string {
	if e.Info.Command != "" {
		return fmt.Sprintf("active run lock exists at %s (command=%s pid=%d started_at=%s)", e.Path, e.Info.Command, e.Info.PID, e.Info.StartedAt.UTC().Format(time.RFC3339))
	}
	if strings.TrimSpace(e.Raw) != "" {
		return fmt.Sprintf("active run lock exists at %s (%s)", e.Path, strings.TrimSpace(e.Raw))
	}
	return fmt.Sprintf("active run lock exists at %s", e.Path)
}

// ResolveLockPath returns the explicit path, then the environment override,
// then the default local orchestrator lock path.
func ResolveLockPath(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if fromEnv := strings.TrimSpace(os.Getenv(EnvLockPath)); fromEnv != "" {
		return fromEnv
	}
	return DefaultLockPath
}

// EnsureInactive fails when a lock exists, unless allow is true.
func EnsureInactive(path string, allow bool) error {
	path = ResolveLockPath(path)
	info, raw, err := readLock(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if allow {
		return nil
	}
	return &ActiveRunError{Path: path, Info: info, Raw: raw}
}

// Lock owns an active-run lock created by Acquire.
type Lock struct {
	path string
}

// Acquire creates the active-run lock for a mutating command. Existing locks
// fail closed unless allow is true.
func Acquire(path, command string, allow bool) (*Lock, error) {
	path = ResolveLockPath(path)
	if err := EnsureInactive(path, allow); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	info := LockInfo{
		Command:   strings.TrimSpace(command),
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	flag := os.O_CREATE | os.O_WRONLY
	if allow {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) && !allow {
			info, raw, readErr := readLock(path)
			if readErr == nil {
				return nil, &ActiveRunError{Path: path, Info: info, Raw: raw}
			}
		}
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &Lock{path: path}, nil
}

// Release removes the lock owned by l.
func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readLock(path string) (LockInfo, string, error) {
	var info LockInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, "", err
	}
	raw := string(data)
	if err := json.Unmarshal(data, &info); err != nil {
		return LockInfo{}, raw, nil
	}
	return info, raw, nil
}
