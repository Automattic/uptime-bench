// Package targetserver implements the failure-injecting HTTP target used by
// uptime-bench. It exposes a TCP proxy front-end (which applies TCP-level
// failures and geo-restricted failures before forwarding) and an HTTP
// virtual-host handler (which applies HTTP-level failures based on Host and
// path). Both consult a shared control.FailureRegistry for active failures.
//
// The package is structured so cmd/target/main.go is a thin wrapper around
// HandleTCP and VirtualHostHandler — almost all real behavior is here, where
// it can be unit-tested.
package targetserver

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

// proxyDialTimeout bounds the dial to the internal HTTP server. It's a var
// (not a const) so tests can shrink it if needed; the production value is
// generous because both ends are on localhost.
var proxyDialTimeout = 5 * time.Second

// ServeProxy runs the TCP-front accept loop on ln. Each accepted connection
// is handed to HandleTCP in its own goroutine. Returns when ln returns a
// permanent accept error (e.g. on close).
func ServeProxy(ln net.Listener, registry *control.FailureRegistry, internalAddr string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("target: accept: %v", err)
			continue
		}
		go HandleTCP(conn, registry, internalAddr)
	}
}

// HandleTCP processes one incoming TCP connection through the proxy layer.
// Check order:
//  1. Geo-restricted failures — intercepted by source IP before any HTTP parsing.
//  2. Global tcp_refused — close immediately with no bytes exchanged.
//  3. Per-host tcp_timeout — peek the Host header, then stall.
//  4. No TCP failure — splice to the internal HTTP server.
func HandleTCP(client net.Conn, registry *control.FailureRegistry, internalAddr string) {
	defer client.Close()

	// Geo failure: if the source IP matches a geographically restricted
	// failure, apply it at the TCP layer and return without forwarding.
	if clientIP := parseRemoteIP(client.RemoteAddr()); clientIP != nil {
		if spec, ok := registry.LookupForIP(clientIP); ok {
			applyGeoFailure(client, spec)
			return
		}
	}

	// tcp_refused is always global — connection refusal happens at SYN time,
	// before any bytes are exchanged, so there's no host to discriminate on.
	if _, ok := registry.Lookup("tcp_refused", "", ""); ok {
		return
	}

	// Peek the HTTP request to extract the Host header for per-host lookup.
	// Peeked bytes stay in the buffer and are replayed transparently on
	// forward. (When TLS lands, the SNI value will replace this.)
	br := bufio.NewReaderSize(client, 4096)
	host := peekHTTPHost(br)

	if spec, ok := registry.Lookup("tcp_timeout", host, ""); ok {
		delay := spec.Duration
		if delay <= 0 {
			delay = 60 * time.Second
		}
		time.Sleep(delay)
		return
	}

	// No TCP failure — forward to the internal HTTP server.
	server, err := net.DialTimeout("tcp", internalAddr, proxyDialTimeout)
	if err != nil {
		log.Printf("target: proxy dial %s: %v", internalAddr, err)
		return
	}
	defer server.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
}

// peekHTTPHost extracts the Host header value from buffered request bytes
// without consuming them. Returns empty string if the Host header isn't
// present in the first 4096 bytes.
func peekHTTPHost(br *bufio.Reader) string {
	if _, err := br.Peek(1); err != nil {
		return ""
	}
	data, _ := br.Peek(br.Buffered())
	// Skip the request line.
	idx := bytes.Index(data, []byte("\r\n"))
	if idx < 0 {
		return ""
	}
	rest := data[idx+2:]
	for {
		nl := bytes.Index(rest, []byte("\r\n"))
		if nl < 0 {
			return ""
		}
		line := rest[:nl]
		if len(line) == 0 {
			return "" // end of headers, no Host found
		}
		// Headers are case-insensitive: match "Host:" prefix loosely.
		if len(line) > 5 && strings.EqualFold(string(line[:5]), "Host:") {
			host := strings.TrimSpace(string(line[5:]))
			if h, _, err := net.SplitHostPort(host); err == nil {
				return h
			}
			return host
		}
		rest = rest[nl+2:]
	}
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
// TCP failure types close or stall the connection; HTTP failure types write
// a minimal well-formed response before closing.
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
		// HTTP failure types from a geo-restricted source: send a minimal
		// HTTP response. Status comes from spec.Params when present.
		code := http.StatusServiceUnavailable
		if v, ok := spec.Params["status_code"]; ok {
			code = paramInt(v, code)
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

// VirtualHostHandler is the HTTP handler running on the internal port.
// It routes by Host header and applies configured HTTP-level failures.
type VirtualHostHandler struct {
	Registry         *control.FailureRegistry
	CapacityObserver *CapacityObserver
}

func (h *VirtualHostHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		host = hostOnly
	}
	path := r.URL.Path
	if h.CapacityObserver != nil {
		ow := &observeResponseWriter{ResponseWriter: w}
		defer func() {
			h.CapacityObserver.RecordResponse(host, r.Method, ow.statusCode, time.Now().UTC())
		}()
		w = ow
	}

	if spec, ok := h.Registry.Lookup("http_method_status", host, path); ok {
		method, _ := spec.Params["method"].(string)
		if strings.EqualFold(method, r.Method) {
			code := paramInt(spec.Params["status_code"], 500)
			http.Error(w, http.StatusText(code), code)
			return
		}
	}

	if spec, ok := h.Registry.Lookup("http_status", host, path); ok {
		code := paramInt(spec.Params["status_code"], 500)
		http.Error(w, http.StatusText(code), code)
		return
	}

	if spec, ok := h.Registry.Lookup("http_timeout", host, path); ok && matchesRequestMethod(spec, r.Method) {
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

	if spec, ok := h.Registry.Lookup("http_latency", host, path); ok && matchesRequestMethod(spec, r.Method) {
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, healthyPageHTML(host, path))
		return
	}

	if spec, ok := h.Registry.Lookup("http_header_status", host, path); ok && matchesRequestMethod(spec, r.Method) {
		if requiredHeadersMatch(r, spec) {
			code := paramInt(spec.Params["status_code"], 403)
			http.Error(w, http.StatusText(code), code)
			return
		}
	}

	if spec, ok := h.Registry.Lookup("http_partial", host, path); ok && matchesRequestMethod(spec, r.Method) {
		truncate := paramInt(spec.Params["truncate_after_bytes"], 64)
		body := healthyPageHTML(host, path)
		w.Header().Set("Content-Type", "text/html")
		// Lie about Content-Length so monitors that check it see a mismatch.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)*2))
		w.WriteHeader(http.StatusOK)
		if truncate < len(body) {
			body = body[:truncate]
		}
		_, _ = w.Write([]byte(body))
		// Best-effort: hijack and close so the partial body isn't auto-padded.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil && conn != nil {
				conn.Close()
			}
		}
		return
	}

	if spec, ok := h.Registry.Lookup("http_body", host, path); ok && matchesRequestMethod(spec, r.Method) {
		content, _ := spec.Params["content"].(string)
		keyword, _ := spec.Params["keyword"].(string)
		// Don't write the 200 header until we know the content variant is
		// recognized — the unknown-variant fallback below needs a 500.
		var body string
		switch content {
		case "empty":
			body = "<html></html>"
		case "error_page":
			body = errorPageHTML
		case "wp_missing_mysql_extension":
			body = wpMissingMySQLExtensionHTML
		case "wp_php_fatal":
			body = wpPHPFatalHTML
		case "wp_allowed_memory":
			body = wpAllowedMemoryHTML
		case "wp_max_execution":
			body = wpMaxExecutionHTML
		case "wp_parse_error":
			body = wpParseErrorHTML
		case "wp_setup_config":
			body = wpSetupConfigHTML
		case "wp_db_repair":
			body = wpDBRepairHTML
		case "wp_db_missing_tables":
			body = wpDBMissingTablesHTML
		case "wp_db_table_crashed":
			body = wpDBTableCrashedHTML
		case "wp_missing_config":
			body = wpMissingConfigHTML
		case "wp_db_update_required":
			body = wpDBUpdateRequiredHTML
		case "wp_maintenance":
			body = wpMaintenanceHTML
		case "wp_unsupported_php":
			body = wpUnsupportedPHPHTML
		case "wp_unsupported_database":
			body = wpUnsupportedDatabaseHTML
		case "wp_unsupported_mariadb":
			body = wpUnsupportedMariaDBHTML
		case "wp_critical_this_website":
			body = wpCriticalThisWebsiteHTML
		case "wp_critical_your_website":
			body = wpCriticalYourWebsiteHTML
		case "wp_technical_this_site":
			body = wpTechnicalThisSiteHTML
		case "wp_technical_the_site":
			body = wpTechnicalTheSiteHTML
		case "apache_default":
			body = apacheDefaultHTML
		case "nginx_default":
			body = nginxDefaultHTML
		case "hosting_suspended":
			body = hostingSuspendedHTML
		case "jetpack_probe":
			body = jetpackProbeHTML
		case "jetpack_probe_compact":
			body = jetpackProbeCompactHTML
		case "xmlrpc_endpoint_echo":
			body = xmlrpcEndpointEchoHTML
		case "wp_directory_listing":
			body = wpDirectoryListingHTML
		case "healthy_fatal_article":
			body = healthyFatalArticleHTML
		case "keyword_missing":
			body = normalPageHTML(host, path, keyword, false)
		case "keyword_injected":
			body = normalPageHTML(host, path, keyword, true)
		case "ransomware":
			body = ransomwareHTML
		case "defacement":
			body = defacementHTML
		case "malicious_script":
			body = maliciousScriptHTML(host, path)
		case "spam_links":
			body = spamLinksHTML(host, path)
		default:
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
		return
	}

	if spec, ok := h.Registry.Lookup("http_redirect", host, path); ok && matchesRequestMethod(spec, r.Method) {
		variant, _ := spec.Params["variant"].(string)
		switch variant {
		case "loop":
			http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
		case "chain":
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

type observeResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *observeResponseWriter) WriteHeader(code int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *observeResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func matchesRequestMethod(spec control.FailureSpec, method string) bool {
	configured, _ := spec.Params["method"].(string)
	if configured == "" {
		return true
	}
	return strings.EqualFold(configured, method)
}

func requiredHeadersMatch(r *http.Request, spec control.FailureSpec) bool {
	if name, _ := spec.Params["header_name"].(string); name != "" {
		if value, ok := spec.Params["header_value"].(string); ok {
			return r.Header.Get(name) == value
		}
		return len(r.Header.Values(name)) > 0
	}
	headers, ok := spec.Params["request_headers"].(map[string]string)
	if ok {
		for k, v := range headers {
			if r.Header.Get(k) != v {
				return false
			}
		}
		return len(headers) > 0
	}
	genericHeaders, ok := spec.Params["request_headers"].(map[string]any)
	if !ok {
		return false
	}
	for k, v := range genericHeaders {
		if r.Header.Get(k) != fmt.Sprint(v) {
			return false
		}
	}
	return len(genericHeaders) > 0
}

// paramInt coerces an interface{} JSON-decoded number to an int with a
// fallback default. JSON numbers come through as float64; explicit ints
// occasionally come through as int when constructed in Go directly.
func paramInt(v any, def int) int {
	switch sv := v.(type) {
	case float64:
		return int(sv)
	case int:
		return sv
	}
	return def
}
