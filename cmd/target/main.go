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
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/targetserver"
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

func main() {
	httpPort := flag.Int("http-port", 80, "port for HTTP monitor traffic")
	controlPort := flag.Int("control-port", 9000, "port for harness control API")
	memberID := flag.String("id", "target", "fleet member ID for control status responses")
	tokenFile := flag.String("token-file", "", "path to control token file (default: CONTROL_TOKEN env)")
	flag.Parse()

	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatalf("target: %v", err)
	}

	registry := control.NewRegistry()

	// Control API server.
	controlSrv := control.NewServer(*memberID, token, registry)
	controlHTTP := &http.Server{
		Addr:    fmt.Sprintf(":%d", *controlPort),
		Handler: controlSrv.Handler(),
	}

	// Internal HTTP data server. Listens on a localhost port that the TCP
	// proxy forwards survivors to. Offsetting the public port by 10000
	// (e.g. 80 → 10080) keeps the mapping obvious in `ss -tlnp`.
	internalPort := *httpPort + 10000
	internalAddr := fmt.Sprintf("127.0.0.1:%d", internalPort)
	dataSrv := &http.Server{
		Addr:    internalAddr,
		Handler: &targetserver.VirtualHostHandler{Registry: registry},
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
}
