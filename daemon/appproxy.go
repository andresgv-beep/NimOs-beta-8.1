package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ═══════════════════════════════════
// App Reverse Proxy
// Routes: /app/{appId}/* → localhost:{port}/*
// Supports: HTTP, WebSocket, SSE
// Solves: HTTPS mixed content, X-Frame-Options, CORS
// ═══════════════════════════════════

func handleAppProxy(w http.ResponseWriter, r *http.Request) {
	// Require authentication for app proxy
	session := authenticate(r)
	if session == nil {
		jsonError(w, 401, "Not authenticated")
		return
	}

	// Block any path traversal attempts
	if strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.RawPath, "..") {
		jsonError(w, 400, "Invalid path")
		return
	}

	// Parse /app/{appId}/...
	path := strings.TrimPrefix(r.URL.Path, "/app/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		jsonError(w, 404, "App not found")
		return
	}

	appId := parts[0]

	// Validate appId — must be alphanumeric/dash only, no IPs, no domains, no traversal
	if strings.Contains(appId, ".") || strings.Contains(appId, ":") ||
		strings.Contains(appId, "/") || strings.Contains(appId, "\\") ||
		strings.Contains(appId, "%") || strings.Contains(appId, "..") {
		jsonError(w, 400, "Invalid app ID")
		return
	}

	subPath := "/"
	if len(parts) > 1 {
		subPath = "/" + parts[1]
	}

	// Find port for this app
	port := getAppPort(appId)
	if port == 0 {
		jsonError(w, 404, fmt.Sprintf("App '%s' not found or has no port", appId))
		return
	}

	// WebSocket upgrade?
	if isWebSocketUpgrade(r) {
		handleWebSocketProxy(w, r, port, subPath)
		return
	}

	// Build target URL
	targetURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, subPath)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Create proxy request
	proxyReq, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		jsonError(w, 500, "Proxy error")
		return
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	proxyReq.Header.Set("Host", r.Host)

	// Execute
	client := &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(502)
		fmt.Fprintf(w, `<html><body style="background:#1c1b3a;color:#fff;display:flex;align-items:center;justify-content:center;height:100vh;font-family:sans-serif"><div style="text-align:center"><h2>%s is not responding</h2><p style="color:#888">Port %d</p><button onclick="location.reload()" style="margin-top:16px;padding:8px 20px;border-radius:8px;border:none;background:#7c6fff;color:#fff;cursor:pointer">Retry</button></div></body></html>`, appId, port)
		return
	}
	defer resp.Body.Close()

	// Copy response headers (strip iframe blockers — apps run inside NimOS iframe)
	// HARD-003: Replace with controlled CSP that only allows our own origin
	for key, values := range resp.Header {
		lower := strings.ToLower(key)
		if lower == "x-frame-options" || lower == "content-security-policy" {
			continue // stripped — we add our own below
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	// Allow framing only from self (NimOS desktop)
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")

	w.WriteHeader(resp.StatusCode)

	// Stream response — support SSE (Server-Sent Events) with flushing
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// handleWebSocketProxy does a raw TCP tunnel for WebSocket connections
func handleWebSocketProxy(w http.ResponseWriter, r *http.Request, port int, subPath string) {
	// Hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		jsonError(w, 500, "WebSocket not supported")
		return
	}

	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		jsonError(w, 500, "Hijack failed")
		return
	}
	defer clientConn.Close()

	// Connect to backend
	backendConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second)
	if err != nil {
		clientConn.Close()
		return
	}
	defer backendConn.Close()

	// Write the original HTTP request to backend (to initiate WS handshake)
	targetPath := subPath
	if r.URL.RawQuery != "" {
		targetPath += "?" + r.URL.RawQuery
	}
	fmt.Fprintf(backendConn, "%s %s HTTP/1.1\r\n", r.Method, targetPath)
	for key, values := range r.Header {
		for _, value := range values {
			fmt.Fprintf(backendConn, "%s: %s\r\n", key, value)
		}
	}
	fmt.Fprintf(backendConn, "\r\n")

	// Flush any buffered data from client
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		clientBuf.Read(buffered)
		backendConn.Write(buffered)
	}

	// Bidirectional copy
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(backendConn, clientConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, backendConn)
		done <- struct{}{}
	}()
	<-done
}

// getAppPort looks up the port for an installed app
func getAppPort(appId string) int {
	if appsRepo == nil {
		return 0
	}
	app, err := appsRepo.GetDockerApp(context.Background(), appId)
	if err != nil || app == nil {
		return 0
	}
	return app.Port
}
