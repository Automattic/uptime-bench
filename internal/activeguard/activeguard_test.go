package activeguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureInactiveFailsWhenLockExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.lock")
	lock, err := Acquire(path, "capacity-run", false)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	err = EnsureInactive(path, false)
	var activeErr *ActiveRunError
	if !errors.As(err, &activeErr) {
		t.Fatalf("EnsureInactive error = %v, want ActiveRunError", err)
	}
	if activeErr.Info.Command != "capacity-run" {
		t.Fatalf("command = %q, want capacity-run", activeErr.Info.Command)
	}
}

func TestEnsureInactiveAllowsOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.lock")
	if err := os.WriteFile(path, []byte("manual lock\n"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if err := EnsureInactive(path, true); err != nil {
		t.Fatalf("EnsureInactive allow=true: %v", err)
	}
}

func TestAcquireUsesExclusiveCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.lock")
	lock, err := Acquire(path, "first", false)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	defer lock.Release()

	_, err = Acquire(path, "second", false)
	var activeErr *ActiveRunError
	if !errors.As(err, &activeErr) {
		t.Fatalf("Acquire second error = %v, want ActiveRunError", err)
	}
}

func TestAcquireOverrideReplacesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.lock")
	first, err := Acquire(path, "first", false)
	if err != nil {
		t.Fatalf("Acquire first: %v", err)
	}
	defer first.Release()

	second, err := Acquire(path, "second", true)
	if err != nil {
		t.Fatalf("Acquire override: %v", err)
	}
	defer second.Release()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if !strings.Contains(string(data), `"command": "second"`) {
		t.Fatalf("lock contents = %s, want replacement command", string(data))
	}
}

func TestResolveLockPathUsesEnvironment(t *testing.T) {
	t.Setenv(EnvLockPath, "/tmp/custom-active.lock")
	if got := ResolveLockPath(""); got != "/tmp/custom-active.lock" {
		t.Fatalf("ResolveLockPath = %q, want env path", got)
	}
	if got := ResolveLockPath("/tmp/explicit.lock"); got != "/tmp/explicit.lock" {
		t.Fatalf("ResolveLockPath explicit = %q", got)
	}
}
