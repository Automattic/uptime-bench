package tokenfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRead_FromFile reads from a file and trims surrounding whitespace.
// Operators paste tokens via sudoedit, which often leaves a trailing
// newline; not trimming would silently break auth.
func TestRead_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  abcdef-the-token\n\n  "), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "abcdef-the-token" {
		t.Fatalf("got %q, want %q", got, "abcdef-the-token")
	}
}

func TestRead_MissingFileReturnsError(t *testing.T) {
	_, err := Read("/nonexistent/path/that/should/never/exist")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "tokenfile") {
		t.Fatalf("error %q should be prefixed with package name", err)
	}
}

// TestRead_EmptyPathFallsBackToEnv — the dns and target binaries pass an
// empty path when the operator hasn't set -token-file, expecting the
// CONTROL_TOKEN env var (set via systemd EnvironmentFile) to be used.
func TestRead_EmptyPathFallsBackToEnv(t *testing.T) {
	t.Setenv("CONTROL_TOKEN", "  env-token\n")
	got, err := Read("")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "env-token" {
		t.Fatalf("got %q, want %q (also expected to be trimmed)", got, "env-token")
	}
}

// TestRead_EmptyPathAndNoEnvErrors — the binary should fail loudly rather
// than start with an empty token (which would let any unauthenticated
// request through).
func TestRead_EmptyPathAndNoEnvErrors(t *testing.T) {
	t.Setenv("CONTROL_TOKEN", "")
	_, err := Read("")
	if err == nil {
		t.Fatal("expected error when both -token-file and CONTROL_TOKEN are empty")
	}
	if !strings.Contains(err.Error(), "no token") {
		t.Fatalf("error %q should make it obvious the token is missing", err)
	}
}

// TestRead_WhitespaceOnlyEnvIsEmpty — a CONTROL_TOKEN containing only
// whitespace should be treated as missing rather than an empty bearer
// token.
func TestRead_WhitespaceOnlyEnvIsEmpty(t *testing.T) {
	t.Setenv("CONTROL_TOKEN", "   \n\t  ")
	_, err := Read("")
	if err == nil {
		t.Fatal("expected error for whitespace-only CONTROL_TOKEN")
	}
}

// TestRead_FileTakesPrecedence — when both a path and the env var are set,
// the file wins. Lets the operator override the systemd-supplied token
// for one-off debugging without restarting the unit.
func TestRead_FileTakesPrecedence(t *testing.T) {
	t.Setenv("CONTROL_TOKEN", "from-env")
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "from-file" {
		t.Fatalf("got %q, want from-file (file should override env)", got)
	}
}
