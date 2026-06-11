package main

import (
	"encoding/json"
	"fmt"
	"net"
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

		CREATE TABLE IF NOT EXISTS shield_whitelist (
			ip TEXT PRIMARY KEY,
			note TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		logMsg("shield DB init error: %v", err)
	}

	dbShieldReputationInit()
}

// ── Whitelist persistence ────────────────────────────────────────────────────
// IPs de confianza que NimShield NUNCA bloquea (p.ej. la IP de auditoría del
// admin). Persisten en BD y se cargan en memoria al arrancar. La fuente de
// verdad en caliente es el mapa shieldWhitelist (shield.go); la BD lo respalda.

func dbShieldWhitelistGetAll() []map[string]string {
	rows, err := db.Query(`SELECT ip, note, created_at FROM shield_whitelist ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var ip, note, created string
		if err := rows.Scan(&ip, &note, &created); err == nil {
			out = append(out, map[string]string{"ip": ip, "note": note, "created_at": created})
		}
	}
	return out
}

func dbShieldWhitelistAdd(ip, note string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO shield_whitelist (ip, note) VALUES (?, ?)`, ip, note)
	return err
}

func dbShieldWhitelistRemove(ip string) error {
	_, err := db.Exec(`DELETE FROM shield_whitelist WHERE ip = ?`, ip)
	return err
}

// loadPersistedWhitelist carga las IPs de confianza de BD al mapa en memoria.
// Se llama al arrancar el shield, junto a loadPersistedBlocks.
func loadPersistedWhitelist() {
	entries := dbShieldWhitelistGetAll()
	shieldBlockMu.Lock()
	for _, e := range entries {
		shieldWhitelist[e["ip"]] = true
	}
	shieldBlockMu.Unlock()
	if len(entries) > 0 {
		logMsg("shield: loaded %d whitelisted IPs", len(entries))
	}
}

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
	rows, err := db.Query(`SELECT id, timestamp, category, severity, source_ip, endpoint, method, rule, details
		FROM shield_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var id int
		var ts, cat, sev, ip, endpoint, method, rule, details string
		rows.Scan(&id, &ts, &cat, &sev, &ip, &endpoint, &method, &rule, &details)
		events = append(events, map[string]interface{}{
			"id": id, "timestamp": ts, "category": cat, "severity": sev,
			"sourceIP": ip, "endpoint": endpoint, "method": method, "rule": rule,
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
	// Control de acceso por APP (no por rol): el admin concede acceso a
	// NimShield desde la gestión de usuarios; quien lo tenga, entra. Mismo
	// modelo que el resto de apps (p.ej. nimtorrent). El loopback y la
	// Lectura (status/events/blocks/whitelist GET): requireAppAccess basta.
	// Mutaciones (unblock/toggle/whitelist POST): exigen rol admin dentro de
	// cada case — apagar el escudo o whitelistar al atacante no puede estar
	// al alcance de cualquier usuario con acceso a la app.
	session := requireAppAccess(w, r, "nimshield")
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
			"enabled":     shieldEnabled,
			"blockedIPs":  blockedCount,
			"honeypots":   len(honeypotPaths),
			"rules":       22,
			"xssPatterns": len(xssPatterns),
			"scannerUAs":  len(scannerUAs),
		})

	// GET /api/shield/events?limit=50
	case path == "/api/shield/events" && method == "GET":
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}
		jsonOk(w, map[string]interface{}{"events": dbShieldEventsRecent(limit)})

	// GET /api/shield/blocks
	case path == "/api/shield/blocks" && method == "GET":
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
			jsonError(w, 403, "Solo un administrador puede modificar NimShield")
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
			jsonError(w, 403, "Solo un administrador puede modificar NimShield")
			return
		}
		shieldEnabled = !shieldEnabled
		logMsg("shield: %s by %s", map[bool]string{true: "enabled", false: "disabled"}[shieldEnabled], session.Username)
		jsonOk(w, map[string]interface{}{"ok": true, "enabled": shieldEnabled})

	// GET /api/shield/whitelist — lista IPs de confianza
	case path == "/api/shield/whitelist" && method == "GET":
		jsonOk(w, map[string]interface{}{"ok": true, "whitelist": dbShieldWhitelistGetAll()})

	// POST /api/shield/whitelist — body: {"ip": "1.2.3.4", "note": "auditoría"}
	case path == "/api/shield/whitelist" && method == "POST":
		if session.Role != "admin" {
			jsonError(w, 403, "Solo un administrador puede modificar NimShield")
			return
		}
		body, _ := readBody(r)
		ip := bodyStr(body, "ip")
		note := bodyStr(body, "note")
		// Validar que es una IP real, no basura arbitraria.
		if net.ParseIP(ip) == nil {
			jsonError(w, 400, "IP inválida")
			return
		}
		if err := dbShieldWhitelistAdd(ip, note); err != nil {
			jsonError(w, 500, "No se pudo guardar")
			return
		}
		// Aplicar en caliente: añadir al mapa en memoria y quitar bloqueo activo.
		shieldBlockMu.Lock()
		shieldWhitelist[ip] = true
		shieldBlockMu.Unlock()
		shieldUnblockIP(ip) // si estaba bloqueada, liberarla ya
		logMsg("shield: whitelisted %s by %s", ip, session.Username)
		jsonOk(w, map[string]interface{}{"ok": true, "ip": ip})

	// POST /api/shield/whitelist/remove — body: {"ip": "1.2.3.4"}
	case path == "/api/shield/whitelist/remove" && method == "POST":
		if session.Role != "admin" {
			jsonError(w, 403, "Solo un administrador puede modificar NimShield")
			return
		}
		body, _ := readBody(r)
		ip := bodyStr(body, "ip")
		if ip == "" {
			jsonError(w, 400, "IP required")
			return
		}
		// No permitir quitar loopback (rompería el acceso local de Caddy).
		if ip == "127.0.0.1" || ip == "::1" {
			jsonError(w, 400, "No se puede quitar loopback de la whitelist")
			return
		}
		if err := dbShieldWhitelistRemove(ip); err != nil {
			jsonError(w, 500, "No se pudo quitar")
			return
		}
		shieldBlockMu.Lock()
		delete(shieldWhitelist, ip)
		shieldBlockMu.Unlock()
		logMsg("shield: un-whitelisted %s by %s", ip, session.Username)
		jsonOk(w, map[string]interface{}{"ok": true, "ip": ip})

	default:
		jsonError(w, 404, "Not found")
	}
}
