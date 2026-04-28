package library

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Automattic/uptime-bench/internal/certmint/manifest"
)

// RemoveArchivedFiles deletes every PEM file referenced by entry's
// Paths and rmdirs the empty ancestor directories that contained
// them. Best-effort: errors are accumulated but never abort the rest
// of the deletion — a half-trim is preferable to a stuck library.
//
// Safe to call on entries whose Paths fields are empty (skips), and
// on entries whose files have already been removed (os.Remove
// returns IsNotExist and we keep going).
func RemoveArchivedFiles(entry manifest.Entry) error {
	var errs []error
	for _, path := range []string{
		entry.Paths.Cert,
		entry.Paths.Chain,
		entry.Paths.FullChain,
		entry.Paths.PrivKey,
	} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}

	// Walk up the directory chain, removing each empty ancestor.
	// Stop at the first directory that's non-empty (Remove returns
	// an error containing ENOTEMPTY) — the rmdir failure is the
	// natural signal that we've reached siblings of this entry.
	dir := ""
	if entry.Paths.Cert != "" {
		dir = filepath.Dir(entry.Paths.Cert)
	} else if entry.Paths.FullChain != "" {
		dir = filepath.Dir(entry.Paths.FullChain)
	}
	for dir != "" && dir != "/" && dir != "." {
		if err := os.Remove(dir); err != nil {
			break // non-empty or permission denied; stop
		}
		dir = filepath.Dir(dir)
	}

	return errors.Join(errs...)
}
