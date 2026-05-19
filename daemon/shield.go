package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// NimShield — Application Security Module
// Phase 1: Collector, Honeypots, Blocklist, Middleware
// ═══════════════════════════════════════════════════════════════════════════════

// ── Shield Event ─────────────────────────────────────────────────────────────

type ShieldEvent struct {
	Timestamp time.Time
	Category  string // auth, traversal, injection, scan, docker, system, honeypot
	Severity  string // low, medium, high, critical
	SourceIP  string
	UserAgent string
	Endpoint  string
	Username  string
	Method    string
	Status    int
	Details   map[string]interface{}
}

var shieldEvents = make(chan ShieldEvent, 2000)

// shieldEmit sends an event to the shield engine. Non-blocking — drops if full.
func shieldEmit(event ShieldEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	select {
	case shieldEvents <- event:
	default:
		// Channel full — drop event rather than block the request
	}
}

// ── Blocklist ────────────────────────────────────────────────────────────────

type BlockEntry struct {
	IP        string
	Reason    string
	Rule      string
	ExpiresAt time.Time
	CreatedAt time.Time
}

var (
	shieldBlocklist = map[string]*BlockEntry{} // IP → entry
	shieldBlockMu   sync.RWMutex
	shieldEnabled   = true
)

func shieldBlockIP(ip string, duration time.Duration, reason, rule string) {
	shieldBlockMu.Lock()
	shieldBlocklist[ip] = &BlockEntry{
		IP:        ip,
		Reason:    reason,
		Rule:      rule,
		ExpiresAt: time.Now().Add(duration),
		CreatedAt: time.Now(),
	}
	shieldBlockMu.Unlock()

	logMsg("shield BLOCK: %s for %v — %s [%s]", ip, duration, reason, rule)

	// Store in DB for persistence across restarts
	dbShieldBlockInsert(ip, duration, reason, rule)

	// Emit notification
	addNotification("warning", "system",
		fmt.Sprintf("IP bloqueada: %s", ip),
		fmt.Sprintf("NimShield bloqueó %s por %v. Motivo: %s", ip, duration, reason))
}

func shieldUnblockIP(ip string) {
	shieldBlockMu.Lock()
	delete(shieldBlocklist, ip)
	shieldBlockMu.Unlock()

	dbShieldBlockDelete(ip)
	logMsg("shield UNBLOCK: %s", ip)
}

func shieldIsBlocked(ip string) (bool, string) {
	shieldBlockMu.RLock()
	entry, exists := shieldBlocklist[ip]
	shieldBlockMu.RUnlock()

	if !exists {
		return false, ""
	}

	// Check expiry
	if time.Now().After(entry.ExpiresAt) {
		shieldUnblockIP(ip)
		return false, ""
	}

	return true, entry.Reason
}

// ── Whitelist ────────────────────────────────────────────────────────────────

var shieldWhitelist = map[string]bool{
	"127.0.0.1": true,
	"::1":       true,
}

func shieldIsWhitelisted(ip string) bool {
	if shieldWhitelist[ip] {
		return true
	}
	// Always whitelist local network
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	_, local24, _ := net.ParseCIDR("192.168.0.0/16")
	_, local20, _ := net.ParseCIDR("172.16.0.0/12")
	_, local8, _ := net.ParseCIDR("10.0.0.0/8")
	if local24.Contains(parsed) || local20.Contains(parsed) || local8.Contains(parsed) {
		return false // LAN IPs are NOT whitelisted by default — only 127.0.0.1
	}
	return false
}

// ── Honeypots ────────────────────────────────────────────────────────────────
// Endpoints that don't exist in NimOS. Any request = 100% malicious.
// Zero false positives. Instant block.

var honeypotPaths = map[string]string{
	"/.env":                     "HONEY-001",
	"/.git/config":              "HONEY-002",
	"/.git/HEAD":                "HONEY-002",
	"/wp-login.php":             "HONEY-003",
	"/wp-admin":                 "HONEY-004",
	"/wp-admin/":                "HONEY-004",
	"/phpmyadmin":               "HONEY-005",
	"/phpmyadmin/":              "HONEY-005",
	"/admin":                    "HONEY-006",
	"/api/admin/debug":          "HONEY-007",
	"/config.json":              "HONEY-008",
	"/api/v1/exec":              "HONEY-009",
	"/shell":                    "HONEY-010",
	"/console":                  "HONEY-011",
	"/actuator":                 "HONEY-012",
	"/actuator/health":          "HONEY-012",
	"/server-status":            "HONEY-013",
	"/xmlrpc.php":               "HONEY-015",
	"/cgi-bin/":                 "HONEY-016",
	"/manager/html":             "HONEY-017",
	"/solr/":                    "HONEY-018",
	"/api/jsonws":               "HONEY-019",
	"/vendor/phpunit":           "HONEY-020",
}

func checkHoneypot(r *http.Request) bool {
	path := strings.ToLower(r.URL.Path)
	rule, isHoneypot := honeypotPaths[path]
	if !isHoneypot {
		// Check prefix matches for paths with trailing content
		for hPath, hRule := range honeypotPaths {
			if strings.HasSuffix(hPath, "/") && strings.HasPrefix(path, hPath) {
				rule = hRule
				isHoneypot = true
				break
			}
		}
	}

	if !isHoneypot {
		return false
	}

	ip := clientIP(r)

	shieldEmit(ShieldEvent{
		Category:  "honeypot",
		Severity:  "critical",
		SourceIP:  ip,
		UserAgent: r.UserAgent(),
		Endpoint:  r.URL.Path,
		Method:    r.Method,
		Details:   map[string]interface{}{"rule": rule},
	})

	// Instant block — 24h, no scoring needed
	if !shieldIsWhitelisted(ip) {
		shieldBlockIP(ip, 24*time.Hour, fmt.Sprintf("Honeypot: %s → %s", r.URL.Path, rule), rule)
	}

	return true
}

// ── XSS/Injection Detection in Requests (CSP Compensation) ──────────────────

var xssPatterns = []string{
	"<script", "</script", "javascript:", "onerror=", "onload=",
	"onfocus=", "onmouseover=", "onclick=", "eval(", "alert(",
	"document.cookie", "document.write", "window.location",
}

var sqliPatterns = []string{
	"' OR ", "' or ", "'; DROP", "'; drop", "UNION SELECT", "union select",
	"1=1", "' AND '", "' and '", "-- ", "/*", "*/",
}

var cmdPatterns = []string{
	"; rm ", "; cat ", "| nc ", "$(", "`", "&& curl", "&& wget",
	"/etc/passwd", "/etc/shadow", "| bash", "| sh",
}

func checkRequestPayload(r *http.Request) string {
	// Check URL query string
	query := r.URL.RawQuery
	if query != "" {
		if matchesPatterns(query, xssPatterns) {
			return "CSP-001"
		}
		if matchesPatterns(query, sqliPatterns) {
			return "INJ-001"
		}
		if matchesPatterns(query, cmdPatterns) {
			return "INJ-002"
		}
	}

	// Check URL path for traversal/injection
	path := r.URL.Path
	if strings.Contains(path, "..") {
		return "TRAV-001"
	}

	return ""
}

func matchesPatterns(input string, patterns []string) bool {
	lower := strings.ToLower(input)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// ── Scanner User-Agent Detection ─────────────────────────────────────────────

var scannerUAs = []string{
	"nikto", "sqlmap", "nmap", "masscan", "dirbuster", "gobuster",
	"wfuzz", "ffuf", "nuclei", "burpsuite", "zaproxy", "acunetix",
	"nessus", "openvas", "w3af", "skipfish", "arachni",
}

func isScannerUA(ua string) bool {
	lower := strings.ToLower(ua)
	for _, s := range scannerUAs {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// ── Shield Middleware ─────────────────────────────────────────────────────────
// Inserted at the very beginning of the HTTP handler chain.
// Returns true if the request was handled (blocked/honeypot) — caller should stop.

func shieldMiddleware(w http.ResponseWriter, r *http.Request) bool {
	if !shieldEnabled {
		return false
	}

	ip := clientIP(r)

	// 1. Check if IP is blocked
	if blocked, reason := shieldIsBlocked(ip); blocked {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"Blocked by NimShield: ` + reason + `"}`))
		return true
	}

	// 2. Check honeypots (instant detection, zero false positives)
	if checkHoneypot(r) {
		http.NotFound(w, r)
		return true
	}

	// 3. Check for known scanner user-agents
	if isScannerUA(r.UserAgent()) && !shieldIsWhitelisted(ip) {
		shieldEmit(ShieldEvent{
			Category:  "scan",
			Severity:  "high",
			SourceIP:  ip,
			UserAgent: r.UserAgent(),
			Endpoint:  r.URL.Path,
			Method:    r.Method,
			Details:   map[string]interface{}{"rule": "SCAN-003", "type": "scanner_ua"},
		})
		shieldBlockIP(ip, 24*time.Hour, "Vulnerability scanner: "+r.UserAgent(), "SCAN-003")
		http.NotFound(w, r)
		return true
	}

	// 4. Check request payload for XSS/SQLi/CMDi (CSP compensation)
	if rule := checkRequestPayload(r); rule != "" && !shieldIsWhitelisted(ip) {
		shieldEmit(ShieldEvent{
			Category:  categoryForRule(rule),
			Severity:  severityForRule(rule),
			SourceIP:  ip,
			UserAgent: r.UserAgent(),
			Endpoint:  r.URL.Path,
			Method:    r.Method,
			Details:   map[string]interface{}{"rule": rule, "query": r.URL.RawQuery},
		})
		// Don't block on first offense for injection — let the rule engine accumulate
		// But DO block immediately for command injection (INJ-002)
		if rule == "INJ-002" {
			shieldBlockIP(ip, 24*time.Hour, "Command injection attempt", rule)
			http.NotFound(w, r)
			return true
		}
	}

	return false
}

func categoryForRule(rule string) string {
	switch {
	case strings.HasPrefix(rule, "AUTH"):
		return "auth"
	case strings.HasPrefix(rule, "TRAV"):
		return "traversal"
	case strings.HasPrefix(rule, "INJ"), strings.HasPrefix(rule, "CSP"):
		return "injection"
	case strings.HasPrefix(rule, "SCAN"):
		return "scan"
	case strings.HasPrefix(rule, "HONEY"):
		return "honeypot"
	default:
		return "system"
	}
}

func severityForRule(rule string) string {
	switch rule {
	case "INJ-002", "HONEY-001":
		return "critical"
	case "INJ-001", "CSP-001", "TRAV-001":
		return "high"
	default:
		return "medium"
	}
}

// ── Shield Engine (background goroutine) ─────────────────────────────────────

func startShieldEngine() {
	if !shieldEnabled {
		logMsg("shield: disabled")
		return
	}

	// Init DB tables
	dbShieldInit()

	// Load persisted blocks
	loadPersistedBlocks()

	logMsg("shield: engine started (honeypots: %d, scanner UAs: %d, XSS patterns: %d)",
		len(honeypotPaths), len(scannerUAs), len(xssPatterns))

	// Process events
	go shieldEventLoop()
}

func shieldEventLoop() {
	for event := range shieldEvents {
		// Store event in DB
		dbShieldEventInsert(event)

		// Run rule engine
		processRules(event)
	}
}

// ── Blocklist persistence ────────────────────────────────────────────────────

func loadPersistedBlocks() {
	blocks := dbShieldBlocksGetActive()
	shieldBlockMu.Lock()
	for _, b := range blocks {
		if b.ExpiresAt.After(time.Now()) {
			shieldBlocklist[b.IP] = &b
		}
	}
	shieldBlockMu.Unlock()
	if len(blocks) > 0 {
		logMsg("shield: loaded %d persisted blocks", len(blocks))
	}
}

// ── Cleanup expired blocks (runs every 5 min) ───────────────────────────────

func startShieldCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		now := time.Now()
		shieldBlockMu.Lock()
		for ip, entry := range shieldBlocklist {
			if now.After(entry.ExpiresAt) {
				delete(shieldBlocklist, ip)
				logMsg("shield: expired block for %s", ip)
			}
		}
		shieldBlockMu.Unlock()

		// Also clean old events from DB (keep 7 days)
		dbShieldEventsCleanup(7 * 24 * time.Hour)
	}
}
