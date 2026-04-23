package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
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

	// Internal HTTP data server (virtual-host handler). Listens on a local
	// port that the TCP proxy forwards traffic to.
	internalPort := *httpPort + 10000 // e.g. 80 → 10080
	dataSrv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", internalPort),
		Handler: &virtualHostHandler{registry: registry},
	}

	go func() {
		log.Printf("target: control API on :%d", *controlPort)
		if err := controlHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("target: control server: %v", err)
		}
	}()

	go func() {
		log.Printf("target: internal HTTP on 127.0.0.1:%d", internalPort)
		if err := dataSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("target: data server: %v", err)
		}
	}()

	// TCP proxy — accepts raw connections on httpPort, checks TCP-level
	// failures, then forwards to the internal HTTP server.
	go func() {
		addr := fmt.Sprintf(":%d", *httpPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("target: listen %s: %v", addr, err)
		}
		log.Printf("target: HTTP proxy on %s → 127.0.0.1:%d", addr, internalPort)
		internalAddr := fmt.Sprintf("127.0.0.1:%d", internalPort)
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("target: accept: %v", err)
				return
			}
			go handleTCP(conn, registry, internalAddr)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("target: shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controlHTTP.Shutdown(ctx)
	dataSrv.Shutdown(ctx)
}

// handleTCP processes one incoming TCP connection through the proxy layer.
// Check order:
//  1. Geo-restricted failures — intercepted here by source IP before any HTTP parsing.
//  2. Global tcp_refused / tcp_timeout — close or stall the connection.
//  3. All other failures — forward to the internal HTTP server.
func handleTCP(client net.Conn, registry *control.FailureRegistry, dst string) {
	defer client.Close()

	// Geo failure: if the source IP matches a geographically restricted failure,
	// apply it at the TCP layer and return without forwarding to the HTTP server.
	if clientIP := parseRemoteIP(client.RemoteAddr()); clientIP != nil {
		if spec, ok := registry.LookupForIP(clientIP); ok {
			applyGeoFailure(client, spec)
			return
		}
	}

	// tcp_refused: always global — connection refused happens at SYN time, before
	// any bytes are exchanged, so there is no host to discriminate on.
	if _, ok := registry.Lookup("tcp_refused", "", ""); ok {
		return
	}

	// Peek at the HTTP request to extract the Host header for per-host lookup.
	// Peeked bytes remain in the buffer and are replayed transparently on forward.
	// When TLS is added, use the SNI value from the ClientHello instead.
	br := bufio.NewReaderSize(client, 4096)
	host := peekHTTPHost(br)

	// tcp_timeout: per-host when a host-specific failure is registered;
	// falls back to global via the registry priority chain.
	if spec, ok := registry.Lookup("tcp_timeout", host, ""); ok {
		delay := spec.Duration
		if delay <= 0 {
			delay = 60 * time.Second
		}
		time.Sleep(delay)
		return
	}

	// No TCP failure — forward to the internal HTTP server.
	server, err := net.DialTimeout("tcp", dst, 5*time.Second)
	if err != nil {
		log.Printf("target: proxy dial %s: %v", dst, err)
		return
	}
	defer server.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(server, br); done <- struct{}{} }()
	go func() { io.Copy(client, server); done <- struct{}{} }()
	<-done
}

// peekHTTPHost extracts the Host header value from the buffered request without
// consuming data. Returns empty string if the host cannot be determined.
func peekHTTPHost(br *bufio.Reader) string {
	data, _ := br.Peek(4096)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Scan() // skip request line
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if len(line) > 5 && strings.EqualFold(line[:5], "Host:") {
			host := strings.TrimSpace(line[5:])
			if h, _, err := net.SplitHostPort(host); err == nil {
				return h
			}
			return host
		}
	}
	return ""
}

// parseRemoteIP extracts the IP address from a net.Addr (e.g. "1.2.3.4:56789").
func parseRemoteIP(addr net.Addr) net.IP {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// applyGeoFailure applies a geo-restricted failure to a raw TCP connection.
// For TCP failure types the connection is closed or stalled. For HTTP failure
// types a minimal HTTP response is written before closing.
func applyGeoFailure(conn net.Conn, spec control.FailureSpec) {
	switch spec.Type {
	case "tcp_refused":
		// Close immediately — no data sent.

	case "tcp_timeout":
		delay := spec.Duration
		if delay <= 0 {
			delay = 60 * time.Second
		}
		time.Sleep(delay)

	default:
		// For HTTP failure types, send a minimal well-formed HTTP response.
		// The status code comes from the failure params when present.
		code := http.StatusServiceUnavailable
		if v, ok := spec.Params["status_code"]; ok {
			switch sv := v.(type) {
			case float64:
				code = int(sv)
			case int:
				code = sv
			}
		}
		statusText := http.StatusText(code)
		if statusText == "" {
			statusText = "Service Unavailable"
		}
		body := statusText + "\n"
		fmt.Fprintf(conn,
			"HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			code, statusText, len(body), body,
		)
	}
}

// virtualHostHandler is the HTTP handler running on the internal port.
// It routes by Host header and applies configured HTTP-level failures.
type virtualHostHandler struct {
	registry *control.FailureRegistry
}

func (h *virtualHostHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	// Strip port from Host if present.
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		host = hostOnly
	}
	path := r.URL.Path

	// http_status — return a configured status code.
	if spec, ok := h.registry.Lookup("http_status", host, path); ok {
		code := 500
		if v, ok := spec.Params["status_code"]; ok {
			switch sv := v.(type) {
			case float64:
				code = int(sv)
			case int:
				code = sv
			}
		}
		http.Error(w, http.StatusText(code), code)
		return
	}

	// http_timeout — delay the response.
	if spec, ok := h.registry.Lookup("http_timeout", host, path); ok {
		delay := spec.Duration
		if d, ok := spec.Params["delay"]; ok {
			if ds, ok := d.(string); ok {
				if pd, err := time.ParseDuration(ds); err == nil {
					delay = pd
				}
			}
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
		return
	}

	// http_partial — write headers then truncate the body.
	if spec, ok := h.registry.Lookup("http_partial", host, path); ok {
		truncate := 64
		if v, ok := spec.Params["truncate_after_bytes"]; ok {
			if fv, ok := v.(float64); ok {
				truncate = int(fv)
			}
		}
		body := healthyPageHTML(host, path)
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)*2)) // lie about length
		w.WriteHeader(http.StatusOK)
		if truncate < len(body) {
			body = body[:truncate]
		}
		w.Write([]byte(body))
		// Close connection abruptly by hijacking — best-effort.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				conn.Close()
			}
		}
		return
	}

	// http_body — serve altered body content.
	if spec, ok := h.registry.Lookup("http_body", host, path); ok {
		content, _ := spec.Params["content"].(string)
		keyword, _ := spec.Params["keyword"].(string)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		switch content {
		case "empty":
			fmt.Fprint(w, "<html></html>")
		case "error_page":
			fmt.Fprint(w, errorPageHTML)
		case "keyword_missing":
			fmt.Fprint(w, normalPageHTML(host, path, keyword, false))
		case "keyword_injected":
			fmt.Fprint(w, normalPageHTML(host, path, keyword, true))
		case "ransomware":
			fmt.Fprint(w, ransomwareHTML)
		case "defacement":
			fmt.Fprint(w, defacementHTML)
		case "malicious_script":
			fmt.Fprint(w, maliciousScriptHTML(host, path))
		case "spam_links":
			fmt.Fprint(w, spamLinksHTML(host, path))
		default:
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	// http_redirect — redirect loop or chain.
	if spec, ok := h.registry.Lookup("http_redirect", host, path); ok {
		variant, _ := spec.Params["variant"].(string)
		switch variant {
		case "loop":
			// Redirect back to the same URL.
			http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
		case "chain":
			// Redirect to a slightly different URL to create a chain.
			http.Redirect(w, r, path+"/redir", http.StatusFound)
		default:
			http.Redirect(w, r, path, http.StatusFound)
		}
		return
	}

	// Healthy response.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, healthyPageHTML(host, path))
}

// healthyPageHTML returns the normal simulated site page for a given host+path.
// It includes a recognisable structure (title, nav, keyword marker) that
// content-inspecting monitors can verify against.
func healthyPageHTML(host, path string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Welcome to %s</title>
</head>
<body>
  <header>
    <h1>Welcome to %s</h1>
    <nav>
      <a href="/">Home</a>
      <a href="/about">About</a>
      <a href="/health">Health Check</a>
    </nav>
  </header>
  <main>
    <p>This site is operational. Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
    <p class="canary">uptime-bench-canary</p>
  </main>
  <footer>
    <p>&copy; %s — uptime-bench benchmark target</p>
  </footer>
</body>
</html>`, host, host, path, host)
}

// normalPageHTML returns the normal page body with or without a specific keyword.
// When inject=false the keyword is omitted (keyword_missing).
// When inject=true the keyword is present (keyword_injected).
func normalPageHTML(host, path, keyword string, inject bool) string {
	extra := ""
	if inject && keyword != "" {
		extra = fmt.Sprintf(`<p class="injected">%s</p>`, keyword)
	}
	// Build the normal page but omit (or include) the requested keyword.
	page := healthyPageHTML(host, path)
	if !inject && keyword != "" {
		// The normal page doesn't contain arbitrary keywords by default,
		// so keyword_missing is already satisfied for any keyword that isn't
		// "Welcome", "Operational", or "uptime-bench-canary". For those
		// built-in keywords we strip the relevant line.
		page = fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s</title>
</head>
<body>
  <header><h1>%s</h1></header>
  <main>
    <p>Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
  </main>
  <footer><p>&copy; %s</p></footer>
</body>
</html>`, host, host, path, host)
	}
	return page + extra
}

// errorPageHTML simulates a CMS "Error establishing a database connection" page
// served with 200 OK — a common WordPress/Drupal silent failure.
const errorPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Error establishing a database connection</title>
</head>
<body>
  <h1>Error establishing a database connection</h1>
  <p>This either means that the username and password information in your
  <code>wp-config.php</code> file is incorrect or we can't contact the
  database server. This could mean your host's database server is down.</p>
  <ul>
    <li>Are you sure you have the correct username and password?</li>
    <li>Are you sure you have typed the correct hostname?</li>
    <li>Are you sure the database server is running?</li>
  </ul>
  <p>If you're unsure what these terms mean you should probably contact your
  host. If you still need help you can always visit the <a href="https://wordpress.org/support/">WordPress Support Forums</a>.</p>
</body>
</html>`

// ransomwareHTML simulates a full site takeover by ransomware/extortion malware.
// Includes realistic markers: BTC address, extortion demand, countdown.
const ransomwareHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>&#x1F512; YOUR WEBSITE HAS BEEN COMPROMISED &#x1F512;</title>
  <style>
    body { background: #0a0a0a; color: #ff3333; font-family: monospace; text-align: center; padding: 40px; }
    h1 { font-size: 2em; text-transform: uppercase; }
    .wallet { background: #1a0000; border: 1px solid #ff3333; padding: 16px; margin: 20px auto; max-width: 600px; word-break: break-all; }
    .timer { font-size: 1.4em; color: #ffaa00; margin: 20px 0; }
    .contact { color: #aaa; margin-top: 30px; }
  </style>
</head>
<body>
  <h1>&#x26A0; YOUR WEBSITE HAS BEEN ENCRYPTED &#x26A0;</h1>
  <p>All files on this server have been encrypted by <strong>DARKLOCK RANSOMWARE v3.1</strong>.</p>
  <p>Your databases, configuration files, and media uploads are no longer accessible.</p>

  <p>To restore your website, send <strong>0.5 BTC</strong> to:</p>
  <div class="wallet">1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf</div>

  <p class="timer">&#x23F1; Time remaining to comply: <span id="t">71:59:47</span></p>

  <p>After payment, email your transaction ID to:</p>
  <p><strong>darklock-recovery@proton.me</strong></p>

  <p class="contact">Do not attempt to restore from backup — we have deleted your snapshots.<br>
  Do not contact law enforcement — we will publish your data.</p>
</body>
</html>`

// defacementHTML simulates a hacktivist web defacement replacing the site.
const defacementHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>H4CK3D</title>
  <style>
    body { background: #000; color: #00ff41; font-family: 'Courier New', monospace; text-align: center; padding: 60px 20px; }
    h1 { font-size: 3em; letter-spacing: 0.2em; text-shadow: 0 0 20px #00ff41; }
    h2 { color: #ff0000; }
    .msg { max-width: 700px; margin: 30px auto; line-height: 1.8; }
    .sig { margin-top: 50px; color: #888; font-size: 0.85em; }
  </style>
</head>
<body>
  <h1>H4CK3D</h1>
  <h2>by 4N0NYM0US C0LL3CT1V3</h2>
  <div class="msg">
    <p>Your security is a joke. Your data belongs to us now.</p>
    <p>This site has been compromised to deliver a message:</p>
    <p><em>"While governments and corporations exploit the digital world,<br>
    we will not remain silent. Free the internet. Free the people."</em></p>
  </div>
  <p class="sig">
    We are Anonymous. We are Legion. We do not forgive. We do not forget.<br>
    Expect us.
  </p>
</body>
</html>`

// maliciousScriptHTML returns an otherwise-normal page with an injected external
// script tag — simulating an XSS or third-party supply-chain compromise.
func maliciousScriptHTML(host, path string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Welcome to %s</title>
</head>
<body>
  <header>
    <h1>Welcome to %s</h1>
    <nav>
      <a href="/">Home</a>
      <a href="/about">About</a>
      <a href="/health">Health Check</a>
    </nav>
  </header>
  <main>
    <p>This site is operational. Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
    <p class="canary">uptime-bench-canary</p>
  </main>
  <footer>
    <p>&copy; %s — uptime-bench benchmark target</p>
  </footer>
  <!-- injected by attacker -->
  <script src="https://cdn.track-analytics-js.example/v2/t.min.js" async></script>
  <script src="https://metrics.evil-cdn.example/collect.js"></script>
</body>
</html>`, host, host, path, host)
}

// spamLinksHTML returns an otherwise-normal page with hidden SEO spam links injected.
func spamLinksHTML(host, path string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Welcome to %s</title>
</head>
<body>
  <header>
    <h1>Welcome to %s</h1>
    <nav>
      <a href="/">Home</a>
      <a href="/about">About</a>
      <a href="/health">Health Check</a>
    </nav>
  </header>
  <main>
    <p>This site is operational. Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
    <p class="canary">uptime-bench-canary</p>
  </main>
  <footer>
    <p>&copy; %s — uptime-bench benchmark target</p>
  </footer>
  <!-- hidden spam links injected by SEO compromise -->
  <div style="display:none;visibility:hidden;height:0;overflow:hidden">
    <a href="http://best-pharmacy-online.example.com/buy-cheap-viagra">buy cheap viagra online no prescription</a>
    <a href="http://casino-slots-winner.example.com">free casino slots no deposit bonus</a>
    <a href="http://crypto-invest-fast.example.com">bitcoin investment platform guaranteed returns</a>
    <a href="http://replica-watches-cheap.example.com">cheap replica designer watches</a>
    <a href="http://online-poker-real-money.example.com">online poker real money usa</a>
  </div>
</body>
</html>`, host, host, path, host)
}
