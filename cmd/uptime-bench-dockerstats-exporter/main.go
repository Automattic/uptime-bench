package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/Automattic/uptime-bench/internal/dockerstats"
)

func main() {
	listen := flag.String("listen", ":9103", "address for Prometheus metrics")
	socketPath := flag.String("docker-socket", "/var/run/docker.sock", "Docker API Unix socket")
	cacheTTL := flag.Duration("cache-ttl", 10*time.Second, "minimum time between Docker API collections")
	timeout := flag.Duration("timeout", 15*time.Second, "Docker API collection timeout")
	maxParallel := flag.Int("max-parallel", 8, "maximum concurrent Docker stats calls")
	flag.Parse()

	exporter := &dockerstats.Exporter{
		Client: &dockerstats.Client{
			SocketPath:  *socketPath,
			MaxParallel: *maxParallel,
		},
		Timeout:  *timeout,
		CacheTTL: *cacheTTL,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		exporter.WritePrometheus(r.Context(), w)
	})
	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("dockerstats exporter listening on %s", *listen)
	log.Fatal(server.ListenAndServe())
}
