package library

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/Automattic/uptime-bench/internal/certmint/manifest"
)

// RegisterServerHandlers wires the read-only cert-library API onto mux:
//
//	GET  /library/manifest.json       — the full manifest.json
//	GET  /library/{entry_id}/{file}   — one PEM file from the entry's
//	                                    archived Paths
//
// {file} is one of cert.pem, chain.pem, fullchain.pem, privkey.pem; any
// other value returns 404 before any disk access. The handler resolves
// disk paths through the manifest (entry.Paths.*), never from the URL,
// so a crafted entry_id can't escape the library directory.
//
// The handler reads the manifest fresh on every file request so a
// daemon writing a new entry mid-poll doesn't strand a target on the
// previous view. Manifest read is one os.Open per request — at the
// fleet's poll cadence this is well under the cost of generating a
// single TLS handshake.
//
// Caller wraps the mux in control.AuthMiddleware so bearer-token auth
// gates every request.
func RegisterServerHandlers(mux *http.ServeMux, manifestPath string) {
	mux.HandleFunc("GET /library/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		serveManifest(w, manifestPath)
	})
	mux.HandleFunc("GET /library/{entry_id}/{file}", func(w http.ResponseWriter, r *http.Request) {
		serveEntryFile(w, r, manifestPath, r.PathValue("entry_id"), r.PathValue("file"))
	})
}

func serveManifest(w http.ResponseWriter, manifestPath string) {
	f, err := os.Open(manifestPath)
	if os.IsNotExist(err) {
		// Empty manifest is the shape manifest.Load returns when the
		// file isn't there. Match that on the wire so a target's
		// first poll against a freshly-provisioned certmint doesn't
		// surface a 404 the operator has to interpret.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest.Manifest{Version: manifest.Version})
		return
	}
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, f)
}

func serveEntryFile(w http.ResponseWriter, r *http.Request, manifestPath, entryID, file string) {
	if entryID == "" {
		http.NotFound(w, r)
		return
	}
	m, err := manifest.Load(manifestPath)
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	var entry *manifest.Entry
	for i := range m.Entries {
		if m.Entries[i].ID == entryID {
			entry = &m.Entries[i]
			break
		}
	}
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	var path string
	switch file {
	case "cert.pem":
		path = entry.Paths.Cert
	case "chain.pem":
		path = entry.Paths.Chain
	case "fullchain.pem":
		path = entry.Paths.FullChain
	case "privkey.pem":
		path = entry.Paths.PrivKey
	default:
		http.NotFound(w, r)
		return
	}
	if path == "" {
		// Manifest entry exists but doesn't carry a path for the
		// requested file (chain.pem can legitimately be empty for
		// some certbot configurations). 404 is the right answer.
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
