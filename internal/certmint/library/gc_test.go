package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Automattic/uptime-bench/internal/certmint/manifest"
)

func TestRemoveArchivedFiles_RemovesPEMsAndEmptyParents(t *testing.T) {
	root := t.TempDir()
	// Mirror the real archive layout:
	//   <root>/<domain>/<profile>/<slot_date>/<slot-dir>/{cert,chain,fullchain,privkey}.pem
	slotDir := filepath.Join(root, "ex.com", "shortlived", "20260428", "slot-00-x-fp")
	if err := os.MkdirAll(slotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(slotDir, "cert.pem")
	chain := filepath.Join(slotDir, "chain.pem")
	full := filepath.Join(slotDir, "fullchain.pem")
	priv := filepath.Join(slotDir, "privkey.pem")
	for _, p := range []string{cert, chain, full, priv} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	entry := manifest.Entry{Paths: manifest.Paths{Cert: cert, Chain: chain, FullChain: full, PrivKey: priv}}
	if err := RemoveArchivedFiles(entry); err != nil {
		t.Fatalf("RemoveArchivedFiles: %v", err)
	}

	for _, p := range []string{cert, chain, full, priv, slotDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", p, err)
		}
	}
	// All ancestors up through .../shortlived/20260428 should be gone too,
	// because the slot dir was the only entry under each.
	for _, p := range []string{
		filepath.Join(root, "ex.com", "shortlived", "20260428"),
		filepath.Join(root, "ex.com", "shortlived"),
		filepath.Join(root, "ex.com"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("ancestor %s should have been rmdir'd: %v", p, err)
		}
	}
}

// TestRemoveArchivedFiles_StopsAtNonEmptyAncestor — when a sibling
// slot exists, the rmdir walk halts at its parent rather than
// erroring or accidentally going further up.
func TestRemoveArchivedFiles_StopsAtNonEmptyAncestor(t *testing.T) {
	root := t.TempDir()
	domainDir := filepath.Join(root, "ex.com", "shortlived", "20260428")
	mySlot := filepath.Join(domainDir, "slot-00-x-fp")
	siblingSlot := filepath.Join(domainDir, "slot-01-y-fp")
	if err := os.MkdirAll(mySlot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siblingSlot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siblingSlot, "cert.pem"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(mySlot, "cert.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	entry := manifest.Entry{Paths: manifest.Paths{Cert: cert, FullChain: cert, PrivKey: cert}}
	if err := RemoveArchivedFiles(entry); err != nil {
		t.Fatalf("RemoveArchivedFiles: %v", err)
	}
	if _, err := os.Stat(mySlot); !os.IsNotExist(err) {
		t.Errorf("my slot dir should be removed: %v", err)
	}
	if _, err := os.Stat(siblingSlot); err != nil {
		t.Errorf("sibling slot dir should remain: %v", err)
	}
	if _, err := os.Stat(domainDir); err != nil {
		t.Errorf("non-empty domain dir should remain: %v", err)
	}
}

func TestRemoveArchivedFiles_TolerantOfMissingFiles(t *testing.T) {
	// Idempotent: running twice doesn't error even though the second
	// run finds the files already gone.
	root := t.TempDir()
	cert := filepath.Join(root, "cert.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := manifest.Entry{Paths: manifest.Paths{Cert: cert, FullChain: cert, PrivKey: cert}}
	if err := RemoveArchivedFiles(entry); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := RemoveArchivedFiles(entry); err != nil {
		t.Fatalf("second call should be a no-op: %v", err)
	}
}
