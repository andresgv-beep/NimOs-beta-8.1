package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// NimShield — Database + HTTP API
// ═══════════════════════════════════════════════════════════════════════════════

// ── DB Init ──────────────────────────────────────────────────────────────────

func dbShieldInit() {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS shield_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			category TEXT NOT NULL,
			severity TEXT NOT NULL,
			source_ip TEXT,
			user_agent TEXT,
			endpoint TEXT,
			username TEXT,
			method TEXT,
			status INTEGER,
			rule TEXT,
			details TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_shield_events_ip ON shield_events(source_ip);
		CREATE INDEX IF NOT EXISTS idx_shield_events_ts ON shield_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_shield_events_cat ON shield_events(category);

		CREATE TABLE IF NOT EXISTS shield_blocks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ip TEXT NOT NULL,
			reason TEXT,
			rule TEXT,
			expires_at TEXT NOT NULL,
			created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_shield_blocks_ip ON shield_blocks(ip);
	`)
	if err != nil {
		logMsg("shield DB init error: %v", err)
	}
}

// ── Event Storage ────────────────────────────────────────────────────────────

func dbShieldEventInsert(event ShieldEvent) {
	rule := ""
	if r, ok := event.Details["rule"].(string); ok {
		rule = r
	}
	detailsJSON := "{}"
	if event.Details != nil {
		if data, err := json.Marshal(event.Details); err == nil {
			detailsJSON = string(data)
		}
	}

	db.Exec(`INSERT INTO shield_events (timestamp, category, severity, source_ip, user_agent, endpoint, username, method, status, rule, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Timestamp.UTC().Format(time.RFC3339),
		event.Category, event.Severity, event.SourceIP,
		event.UserAgent, event.Endpoint, event.Username,
		event.Method, event.Status, rule, detailsJSON)
}

func dbShieldEventsCleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)
	db.Exec(`DELETE FROM shield_events WHERE timestamp < ?`, cutoff)
}

func dbShieldEventsRecent(limit int) []map[string]interface{} {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(`SELECT id, timestamp, category, severity, source_ip, endpoint, rule, details
		FROM shield_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var id int
		var ts, cat, sev, ip, endpoint, rule, details string
		rows.Scan(&id, &ts, &cat, &sev, &ip, &endpoint, &rule, &details)
		events = append(events, map[string]interface{}{
			"id": id, "timestamp": ts, "category": cat, "severity": sev,
			"sourceIP": ip, "endpoint": endpoint, "rule": rule,
		})
	}
	if events == nil {
		events = []map[string]interface{}{}
	}
	return events
}

// ── Block Storage ────────────────────────────────────────────────────────────

func dbShieldBlockInsert(ip string, duration time.Duration, reason, rule string) {
	expiresAt := time.Now().Add(duration).UTC().Format(time.RFC3339)
	// Upsert — replace if IP already blocked
	db.Exec(`DELETE FROM shield_blocks WHERE ip = ?`, ip)
	db.Exec(`INSERT INTO shield_blocks (ip, reason, rule, expires_at) VALUES (?, ?, ?, ?)`,
		ip, reason, rule, expiresAt)
}

func dbShieldBlockDelete(ip string) {
	db.Exec(`DELETE FROM shield_blocks WHERE ip = ?`, ip)
}

func dbShieldBlocksGetActive() []BlockEntry {
	rows, err := db.Query(`SELECT ip, reason, rule, expires_at, created_at FROM shield_blocks WHERE expires_at > datetime('now')`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var blocks []BlockEntry
	for rows.Next() {
		var b BlockEntry
		var expiresStr, createdStr string
		rows.Scan(&b.IP, &b.Reason, &b.Rule, &expiresStr, &createdStr)
		b.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
		b.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		blocks = append(blocks, b)
	}
	return blocks
}

// ── HTTP API ─────────────────────────────────────────────────────────────────

func handleShieldRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	path := r.URL.Path
	method := r.Method

	switch {

	// GET /api/shield/status
	case path == "/api/shield/status" && method == "GET":
		shieldBlockMu.RLock()
		blockedCount := len(shieldBlocklist)
		shieldBlockMu.RUnlock()

		jsonOk(w, map[string]interface{}{
			"enabled":      shieldEnabled,
			"blockedIPs":   blockedCount,
			"honeypots":    len(honeypotPaths),
			"rules":        22,
			"xssPatterns":  len(xssPatterns),
			"scannerUAs":   len(scannerUAs),
		})

	// GET /api/shield/events?limit=50
	case path == "/api/shield/events" && method == "GET":
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required")
			return
		}
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}
		jsonOk(w, map[string]interface{}{"events": dbShieldEventsRecent(limit)})

	// GET /api/shield/blocks
	case path == "/api/shield/blocks" && method == "GET":
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required")
			return
		}
		shieldBlockMu.RLock()
		blocks := make([]map[string]interface{}, 0, len(shieldBlocklist))
		for ip, entry := range shieldBlocklist {
			blocks = append(blocks, map[string]interface{}{
				"ip":        ip,
				"reason":    entry.Reason,
				"rule":      entry.Rule,
				"expiresAt": entry.ExpiresAt.UTC().Format(time.RFC3339),
				"createdAt": entry.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		shieldBlockMu.RUnlock()
		jsonOk(w, map[string]interface{}{"blocks": blocks})

	// POST /api/shield/unblock — body: {"ip": "1.2.3.4"}
	case path == "/api/shield/unblock" && method == "POST":
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required")
			return
		}
		body, _ := readBody(r)
		ip := bodyStr(body, "ip")
		if ip == "" {
			jsonError(w, 400, "IP required")
			return
		}
		shieldUnblockIP(ip)
		jsonOk(w, map[string]interface{}{"ok": true, "ip": ip})

	// POST /api/shield/toggle — enable/disable
	case path == "/api/shield/toggle" && method == "POST":
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required")
			return
		}
		shieldEnabled = !shieldEnabled
		logMsg("shield: %s by %s", map[bool]string{true: "enabled", false: "disabled"}[shieldEnabled], session.Username)
		jsonOk(w, map[string]interface{}{"ok": true, "enabled": shieldEnabled})

	default:
		jsonError(w, 404, "Not found")
	}
}
