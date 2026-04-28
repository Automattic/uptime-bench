// Command uptime-bench-target is the failure-injecting HTTP target.
//
// It listens on the configured HTTP port (default :80), accepts TCP
// connections through a small proxy that applies TCP-level and
// geo-restricted failures, and forwards survivors to an internal HTTP
// handler that applies HTTP-level failures based on Host and path.
// All real behavior lives in internal/targetserver; this binary is wiring.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/certlibrary"
	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/targetserver"
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

func main() {
	httpPort := flag.Int("http-port", 80, "port for HTTP monitor traffic")
	httpsPort := flag.Int("https-port", 443, "port for HTTPS monitor traffic; set 0 to disable")
	controlPort := flag.Int("control-port", 9000, "port for harness control API")
	memberID := flag.String("id", "target", "fleet member ID for control status responses")
	tlsHosts := flag.String("tls-hosts", "localhost,bench.local,probe.local", "comma-separated SANs for the generated default self-signed HTTPS certificate")
	tlsMismatchHost := flag.String("tls-mismatch-host", "uptime-bench-invalid.local", "SAN for the generated tls_invalid hostname_mismatch certificate")
	certLibraryManifest := flag.String("cert-library-manifest", "", "path to a local uptime-bench-certmint manifest.json — for ad-hoc tests; production targets pull from -cert-library-source instead")
	certLibrarySource := flag.String("cert-library-source", "", "base URL of certmint's cert-library HTTP API, e.g. http://certmint-01.bench:9200; when set, the target polls it on -cert-library-poll-interval and atomically swaps the in-memory library on each successful fetch")
	certLibraryPollInterval := flag.Duration("cert-library-poll-interval", 30*time.Minute, "how often to re-fetch the cert library from -cert-library-source")
	certLibraryCacheDir := flag.String("cert-library-cache-dir", "/var/cache/uptime-bench-target/cert-library", "local directory the polled library mirrors into")
	tokenFile := flag.String("token-file", "", "path to control token file (default: CONTROL_TOKEN env)")
	flag.Parse()

	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatalf("target: %v", err)
	}

	registry := control.NewRegistry()

	dataHandler := &targetserver.VirtualHostHandler{Registry: registry}

	// Internal HTTP data server. Listens on a localhost port that the TCP
	// proxy forwards survivors to. Offsetting the public port by 10000
	// (e.g. 80 → 10080) keeps the mapping obvious in `ss -tlnp`.
	internalPort := *httpPort + 10000
	internalAddr := fmt.Sprintf("127.0.0.1:%d", internalPort)
	dataSrv := &http.Server{
		Addr:    internalAddr,
		Handler: dataHandler,
	}

	var (
		httpsHTTP         *http.Server
		certLibController *targetserver.CertLibraryController
	)
	if *httpsPort > 0 {
		cert, err := targetserver.SelfSignedCertificate(splitCSV(*tlsHosts), time.Now())
		if err != nil {
			log.Fatalf("target: tls cert: %v", err)
		}
		mismatchCert, err := targetserver.SelfSignedCertificate([]string{*tlsMismatchHost}, time.Now())
		if err != nil {
			log.Fatalf("target: tls mismatch cert: %v", err)
		}
		certSelector := &targetserver.CertificateSelector{
			Registry:         registry,
			Fallback:         cert,
			HostnameMismatch: mismatchCert,
		}
		if *certLibraryManifest != "" {
			loaded, err := certlibrary.Load(*certLibraryManifest)
			if err != nil {
				log.Fatalf("target: load cert library: %v", err)
			}
			certSelector.SetLibrary(&loaded)
			log.Printf("target: loaded cert library %s entries=%d", *certLibraryManifest, len(loaded.Entries))
		}
		certLibController = &targetserver.CertLibraryController{
			Selector:            certSelector,
			Token:               token,
			CacheDir:            *certLibraryCacheDir,
			DefaultPollInterval: *certLibraryPollInterval,
		}
		if *certLibrarySource != "" {
			// -cert-library-source is the dev path (operator
			// passes a flag). Production targets receive the same
			// config via the harness control plane; the flag and
			// the control plane share the same controller so
			// either path produces the same running poll loop.
			if err := certLibController.Apply(targetserver.CertLibraryConfigRequest{URL: *certLibrarySource}); err != nil {
				log.Fatalf("target: cert-library: %v", err)
			}
		}
		baseTLS := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		configSelector := &targetserver.TLSConfigSelector{
			Registry:     registry,
			Certificates: certSelector,
			Base:         baseTLS,
		}
		httpsHTTP = &http.Server{
			Addr:    fmt.Sprintf(":%d", *httpsPort),
			Handler: dataHandler,
			TLSConfig: &tls.Config{
				GetConfigForClient: configSelector.GetConfigForClient,
			},
		}
	}

	// Compose the control mux with both the standard fleet-control
	// routes and the target-specific cert-library config endpoint
	// behind a single auth wrap, mirroring how the dns binary mounts
	// its ACME endpoints alongside control.
	controlSrv := control.NewServer(*memberID, token, registry)
	controlMux := http.NewServeMux()
	controlSrv.RegisterRoutes(controlMux)
	if certLibController != nil {
		targetserver.RegisterCertLibraryConfigHandler(controlMux, certLibController)
	}
	controlHTTP := &http.Server{
		Addr:    fmt.Sprintf(":%d", *controlPort),
		Handler: control.AuthMiddleware(token)(controlMux),
	}

	go func() {
		log.Printf("target: control API on :%d", *controlPort)
		if err := controlHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("target: control server: %v", err)
		}
	}()

	go func() {
		log.Printf("target: internal HTTP on %s", internalAddr)
		if err := dataSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("target: data server: %v", err)
		}
	}()

	if httpsHTTP != nil {
		go func() {
			log.Printf("target: HTTPS on :%d", *httpsPort)
			if err := httpsHTTP.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("target: https server: %v", err)
			}
		}()
	}

	// TCP front: applies TCP-level and geo-restricted failures, then
	// splices to the internal HTTP server.
	addr := fmt.Sprintf(":%d", *httpPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("target: listen %s: %v", addr, err)
	}
	defer ln.Close()
	log.Printf("target: HTTP proxy on %s → %s", addr, internalAddr)
	go targetserver.ServeProxy(ln, registry, internalAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("target: shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := controlHTTP.Shutdown(ctx); err != nil {
		log.Printf("target: control shutdown: %v", err)
	}
	if err := dataSrv.Shutdown(ctx); err != nil {
		log.Printf("target: data shutdown: %v", err)
	}
	if httpsHTTP != nil {
		if err := httpsHTTP.Shutdown(ctx); err != nil {
			log.Printf("target: https shutdown: %v", err)
		}
	}
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
