package main

import (
	"flag"
	"log"
)

func main() {
	httpPort := flag.Int("http-port", 80, "port for HTTP monitor traffic")
	httpsPort := flag.Int("https-port", 443, "port for HTTPS monitor traffic")
	controlPort := flag.Int("control-port", 9000, "port for harness control API")
	flag.Parse()

	log.Printf("target: starting (http :%d, https :%d, control :%d)",
		*httpPort, *httpsPort, *controlPort)
	// TODO: start TCP proxy, HTTP server, control API
}
