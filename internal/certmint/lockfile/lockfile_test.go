package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certmint.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() first error = %v", err)
	}
	defer first.Release()

	second, err := Acquire(path)
	if err == nil {
		second.Release()
		t.Fatal("Acquire() second error = nil, want ErrLocked")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Acquire() second error = %v, want ErrLocked", err)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certmint.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() first error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	defer second.Release()
}

func TestAcquireWritesMetadataWithRestrictedPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "certmint.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer lock.Release()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("lock mode = %o, want 600", gotMode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "pid=") || !strings.Contains(content, "acquired_at=") {
		t.Fatalf("lock metadata = %q", content)
	}
}
