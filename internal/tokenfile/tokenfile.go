// Package tokenfile reads a bearer token from a file or environment variable.
package tokenfile

import (
	"fmt"
	"os"
	"strings"
)

// Read reads a token from the given file path.
// If path is empty, it falls back to the CONTROL_TOKEN environment variable.
func Read(path string) (string, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("tokenfile: read %s: %w", path, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	tok := strings.TrimSpace(os.Getenv("CONTROL_TOKEN"))
	if tok == "" {
		return "", fmt.Errorf("tokenfile: no token: set -token-file flag or CONTROL_TOKEN env var")
	}
	return tok, nil
}
