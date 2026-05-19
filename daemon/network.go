package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// runShellLongStatic executes a STATIC command with a custom timeout (no retry).
// SECURITY: Rejects interpolated commands. Use runSafeLong for dynamic args.
func runShellLongStatic(command string, timeoutSecs int) (string, bool) {
	if strings.Contains(command, "%s") || strings.Contains(command, "%d") || strings.Contains(command, "%v") {
		logMsg("SECURITY: runShellLongStatic rejected interpolated command: %s", command)
		return "", false
	}
	ctx := exec.Command("sh", "-c", command)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = ctx.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err == nil
	case <-time.After(time.Duration(timeoutSecs) * time.Second):
		ctx.Process.Kill()
		return "timeout", false
	}
}

// runSafeLong executes a command with args directly (no shell) with a custom timeout.
func runSafeLong(cmd string, args []string, timeoutSecs int) (string, bool) {
	c := exec.Command(cmd, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = c.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err == nil
	case <-time.After(time.Duration(timeoutSecs) * time.Second):
		c.Process.Kill()
		return "timeout", false
	}
}

// ═══════════════════════════════════
// Config files
// ═══════════════════════════════════

const (
	ddnsConfigFile         = "/var/lib/nimos/config/ddns.json"
	ddnsLogFile            = "/var/lib/nimos/config/ddns.log"
	remoteAccessConfigFile = "/var/lib/nimos/config/remote-access.json"
	smbConfigFile          = "/var/lib/nimos/config/smb.json"
	proxyConfigFile        = "/var/lib/nimos/config/proxy-rules.json"
	webdavConfigFile       = "/var/lib/nimos/config/webdav.json"
)

func readJSONConfig(path string, defaults map[string]interface{}) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaults
	}
	var conf map[string]interface{}
	if json.Unmarshal(data, &conf) != nil {
		return defaults
	}
	return conf
}

func writeJSONConfig(path string, conf interface{}) {
	data, _ := json.MarshalIndent(conf, "", "  ")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, data, 0644)
}

// ═══════════════════════════════════
// DDNS
// ═══════════════════════════════════

func handleDdnsRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	path := r.URL.Path
	method := r.Method

	if path == "/api/ddns/status" && method == "GET" {
		conf := readJSONConfig(ddnsConfigFile, map[string]interface{}{"enabled": false})
		extIp, _ := runSafe("curl", "-fsSL", "--connect-timeout", "5", "https://api.ipify.org")
		if extIp == "" {
			extIp, _ = runSafe("curl", "-fsSL", "--connect-timeout", "5", "https://ifconfig.me")
		}
		lastLog := ""
		if data, err := os.ReadFile(ddnsLogFile); err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) > 0 {
				lastLog = lines[len(lines)-1]
			}
		}
		jsonOk(w, map[string]interface{}{"config": conf, "externalIp": strings.TrimSpace(extIp), "lastLog": lastLog})
		return
	}

	if path == "/api/ddns/config" && method == "POST" {
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required"); return
		}
		body, _ := readBody(r)
		writeJSONConfig(ddnsConfigFile, body)
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	if path == "/api/ddns/test" && method == "POST" {
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required"); return
		}
		body, _ := readBody(r)
		jsonOk(w, ddnsUpdateGo(body))
		return
	}

	if path == "/api/ddns/logs" && method == "GET" {
		log := ""
		if data, err := os.ReadFile(ddnsLogFile); err == nil {
			log = string(data)
		}
		jsonOk(w, map[string]interface{}{"log": log})
		return
	}

	jsonError(w, 404, "Not found")
}

func ddnsUpdateGo(cfg map[string]interface{}) map[string]interface{} {
	provider := bodyStr(cfg, "provider")
	domain := strings.TrimSpace(bodyStr(cfg, "domain"))
	token := strings.TrimSpace(bodyStr(cfg, "token"))

	// SECURITY: validate domain and token to prevent injection
	if !isValidDomain(domain) {
		return map[string]interface{}{"ok": false, "error": "Invalid domain format"}
	}
	if token == "" || len(token) > 256 || strings.ContainsAny(token, "\"'`;|&<>$\\") {
		return map[string]interface{}{"ok": false, "error": "Invalid token format"}
	}

	var curlURL string
	switch provider {
	case "duckdns":
		subdomain := strings.Replace(domain, ".duckdns.org", "", 1)
		curlURL = fmt.Sprintf("https://www.duckdns.org/update?domains=%s&token=%s&ip=", subdomain, token)
	case "noip":
		curlURL = fmt.Sprintf("https://dynupdate.no-ip.com/nic/update?hostname=%s", domain)
	case "dynu":
		curlURL = fmt.Sprintf("https://api.dynu.com/nic/update?hostname=%s&password=%s", domain, token)
	case "freedns":
		curlURL = fmt.Sprintf("https://freedns.afraid.org/dynamic/update.php?%s", token)
	default:
		return map[string]interface{}{"ok": false, "error": "Unknown provider"}
	}

	result, ok := runSafe("curl", "-fsSL", curlURL)
	if ok {
		return map[string]interface{}{"ok": true, "response": strings.TrimSpace(result)}
	}
	return map[string]interface{}{"ok": false, "error": result}
}

// ═══════════════════════════════════
// Remote Access
// ═══════════════════════════════════

func handleRemoteAccessRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}
	urlPath := r.URL.Path
	method := r.Method

	if urlPath == "/api/remote-access/status" && method == "GET" {
		cfg := readJSONConfig(remoteAccessConfigFile, map[string]interface{}{
			"ddns": map[string]interface{}{"enabled": false}, "ssl": map[string]interface{}{"enabled": false},
			"https": map[string]interface{}{"enabled": false, "port": float64(5009)},
		})
		status := getRemoteAccessStatusGo(cfg)
		status["config"] = cfg
		jsonOk(w, status)
		return
	}

	if urlPath == "/api/remote-access/configure" && method == "POST" {
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required"); return
		}
		body, _ := readBody(r)
		cfg := readJSONConfig(remoteAccessConfigFile, map[string]interface{}{})
		if ddns, ok := body["ddns"].(map[string]interface{}); ok {
			existing, _ := cfg["ddns"].(map[string]interface{})
			if existing == nil { existing = map[string]interface{}{} }
			for k, v := range ddns { existing[k] = v }
			cfg["ddns"] = existing
		}
		if ssl, ok := body["ssl"].(map[string]interface{}); ok {
			existing, _ := cfg["ssl"].(map[string]interface{})
			if existing == nil { existing = map[string]interface{}{} }
			for k, v := range ssl { existing[k] = v }
			cfg["ssl"] = existing
		}
		if https, ok := body["https"].(map[string]interface{}); ok {
			existing, _ := cfg["https"].(map[string]interface{})
			if existing == nil { existing = map[string]interface{}{} }
			for k, v := range https { existing[k] = v }
			cfg["https"] = existing
		}
		writeJSONConfig(remoteAccessConfigFile, cfg)
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	if urlPath == "/api/remote-access/test-ddns" && method == "POST" {
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required"); return
		}
		body, _ := readBody(r)
		jsonOk(w, ddnsUpdateGo(body))
		return
	}

	if urlPath == "/api/remote-access/request-ssl" && method == "POST" {
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required"); return
		}
		body, _ := readBody(r)
		domain := bodyStr(body, "domain")
		email := bodyStr(body, "email")
		certMethod := bodyStr(body, "method")
		provider := bodyStr(body, "provider")
		dnsToken := bodyStr(body, "dnsToken")
		if domain == "" || email == "" {
			jsonError(w, 400, "Domain and email required"); return
		}
		// SECURITY: validate domain and email to prevent injection
		if !isValidDomain(domain) {
			jsonError(w, 400, "Invalid domain format"); return
		}
		if !isValidEmail(email) {
			jsonError(w, 400, "Invalid email format"); return
		}

		args := []string{"certonly", "--non-interactive", "--agree-tos", "-m", email}
		if certMethod == "dns" && provider == "duckdns" {
			subdomain := strings.Replace(domain, ".duckdns.org", "", 1)
			if !reAlphanumDash.MatchString(subdomain) {
				jsonError(w, 400, "Invalid DuckDNS subdomain"); return
			}
			// SECURITY: dnsToken goes into a bash script — must be validated
			if dnsToken == "" || len(dnsToken) > 256 || strings.ContainsAny(dnsToken, "\"'`;|&<>$\\(){}[]!#~") {
				jsonError(w, 400, "Invalid DNS token format"); return
			}
			hookDir := filepath.Join(configDir, "certbot-hooks")
			os.MkdirAll(hookDir, 0755)
			authHook := filepath.Join(hookDir, "duckdns-auth.sh")
			os.WriteFile(authHook, []byte(fmt.Sprintf("#!/bin/bash\ncurl -s \"https://www.duckdns.org/update?domains=%s&token=%s&txt=$CERTBOT_VALIDATION\" > /dev/null\nsleep 60\n", subdomain, dnsToken)), 0755)
			cleanupHook := filepath.Join(hookDir, "duckdns-cleanup.sh")
			os.WriteFile(cleanupHook, []byte(fmt.Sprintf("#!/bin/bash\ncurl -s \"https://www.duckdns.org/update?domains=%s&token=%s&txt=removed&clear=true\" > /dev/null\n", subdomain, dnsToken)), 0755)
			args = append(args, "--manual", "--preferred-challenges", "dns", "--manual-auth-hook", authHook, "--manual-cleanup-hook", cleanupHook, "-d", domain)
		} else if certMethod == "standalone" {
			args = append(args, "--standalone", "-d", domain)
		} else {
			args = append(args, "--webroot", "-w", "/var/www/html", "-d", domain)
		}

		// Certbot needs long timeout (DNS propagation = 60s+)
		log, ok := runSafeLong("sudo", args, 180)
		if ok {
			cfg := readJSONConfig(remoteAccessConfigFile, map[string]interface{}{})
			ssl, _ := cfg["ssl"].(map[string]interface{})
			if ssl == nil { ssl = map[string]interface{}{} }
			ssl["enabled"] = true
			ssl["domain"] = domain
			cfg["ssl"] = ssl
			writeJSONConfig(remoteAccessConfigFile, cfg)
			jsonOk(w, map[string]interface{}{"ok": true, "log": log})
		} else {
			jsonError(w, 500, "Certificate request failed")
		}
		return
	}

	if urlPath == "/api/remote-access/enable-https" && method == "POST" {
		if session.Role != "admin" {
			jsonError(w, 403, "Admin required"); return
		}
		body, _ := readBody(r)
		domain := bodyStr(body, "domain")
		portF, _ := body["port"].(float64)
		httpsPort := int(portF)
		if httpsPort == 0 { httpsPort = 5009 }
		enabled, _ := body["enabled"].(bool)

		cfg := readJSONConfig(remoteAccessConfigFile, map[string]interface{}{})

		if enabled {
			certDir := fmt.Sprintf("/etc/letsencrypt/live/%s", domain)
			if _, err := os.Stat(certDir + "/fullchain.pem"); err != nil {
				jsonError(w, 400, fmt.Sprintf("No certificate found for %s", domain)); return
			}
			nginxConf := fmt.Sprintf(`server {
    listen %d ssl http2;
    listen [::]:%d ssl http2;
    server_name %s;
    ssl_certificate %s/fullchain.pem;
    ssl_certificate_key %s/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
        proxy_request_buffering off;
        client_max_body_size 0;
    }
}`, httpsPort, httpsPort, domain, certDir, certDir)
			os.WriteFile("/etc/nginx/sites-available/nimos-https.conf", []byte(nginxConf), 0644)
			runSafe("ln", "-sf", "/etc/nginx/sites-available/nimos-https.conf", "/etc/nginx/sites-enabled/nimos-https.conf")
			runSafe("sudo", "ufw", "allow", fmt.Sprintf("%d/tcp", httpsPort))
			runShellStatic("sudo nginx -t 2>/dev/null && sudo systemctl reload nginx")
			https := map[string]interface{}{"enabled": true, "port": httpsPort}
			cfg["https"] = https
			writeJSONConfig(remoteAccessConfigFile, cfg)
			jsonOk(w, map[string]interface{}{"ok": true, "message": fmt.Sprintf("HTTPS enabled on port %d", httpsPort)})
		} else {
			runSafe("rm", "-f", "/etc/nginx/sites-enabled/nimos-https.conf")
			runSafe("sudo", "systemctl", "reload", "nginx")
			cfg["https"] = map[string]interface{}{"enabled": false, "port": httpsPort}
			writeJSONConfig(remoteAccessConfigFile, cfg)
			jsonOk(w, map[string]interface{}{"ok": true, "message": "HTTPS disabled"})
		}
		return
	}

	jsonError(w, 404, "Not found")
}

func getRemoteAccessStatusGo(cfg map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"ddns":  map[string]interface{}{"working": false, "externalIp": nil},
		"ssl":   map[string]interface{}{"valid": false},
		"https": map[string]interface{}{"running": false, "enabled": false},
	}
	extIp, _ := runSafe("curl", "-fsSL", "--connect-timeout", "5", "https://api.ipify.org")
	ddnsConf, _ := cfg["ddns"].(map[string]interface{})
	if ddnsConf != nil {
		result["ddns"] = ddnsConf
		ddnsMap := result["ddns"].(map[string]interface{})
		ddnsMap["externalIp"] = strings.TrimSpace(extIp)
	}

	// Check SSL certificate on disk
	fullDomain := ""
	if ddnsConf != nil {
		if fd, ok := ddnsConf["fullDomain"].(string); ok && fd != "" {
			fullDomain = fd
		} else if d, ok := ddnsConf["domain"].(string); ok {
			fullDomain = d
		}
	}
	if sslConf, _ := cfg["ssl"].(map[string]interface{}); sslConf != nil {
		if d, ok := sslConf["domain"].(string); ok && d != "" {
			fullDomain = d
		}
	}
	if fullDomain != "" {
		certDir := fmt.Sprintf("/etc/letsencrypt/live/%s", fullDomain)
		certPath := certDir + "/fullchain.pem"
		if _, err := os.Stat(certPath); err == nil {
			// Cert exists — check expiry
			certInfo, _ := runSafe("openssl", "x509", "-in", certPath, "-noout", "-enddate")
			daysLeft := -1
			expiry := ""
			if certInfo != "" {
				reExpiry := regexp.MustCompile(`notAfter=(.+)`)
				if m := reExpiry.FindStringSubmatch(certInfo); m != nil {
					expiry = strings.TrimSpace(m[1])
					if t, err := time.Parse("Jan  2 15:04:05 2006 MST", expiry); err == nil {
						daysLeft = int(time.Until(t).Hours() / 24)
					} else if t, err := time.Parse("Jan 2 15:04:05 2006 MST", expiry); err == nil {
						daysLeft = int(time.Until(t).Hours() / 24)
					}
				}
			}
			result["ssl"] = map[string]interface{}{
				"valid":    daysLeft > 0,
				"domain":   fullDomain,
				"expiry":   expiry,
				"daysLeft": daysLeft,
				"certPath": certPath,
				"keyPath":  certDir + "/privkey.pem",
			}
		}
	}

	// Check HTTPS
	httpsConf, _ := cfg["https"].(map[string]interface{})
	if httpsConf != nil {
		result["https"] = httpsConf
		if enabled, _ := httpsConf["enabled"].(bool); enabled {
			portF, _ := httpsConf["port"].(float64)
			port := int(portF)
			if port == 0 { port = 5009 }
			ssOut, _ := runSafe("ss", "-tlnp")
			portStr := fmt.Sprintf(":%d ", port)
			listening := strings.Contains(ssOut, portStr)
			httpsMap := result["https"].(map[string]interface{})
			httpsMap["running"] = listening
		}
	}

	// Local IP
	if lip, ok := runShellStatic("hostname -I 2>/dev/null | awk '{print $1}'"); ok {
		result["localIp"] = strings.TrimSpace(lip)
	}
	result["nimosPort"] = 5000
	return result
}

// ═══════════════════════════════════
// SSH
// ═══════════════════════════════════

func handleSshRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	switch {
	case r.URL.Path == "/api/ssh/status" && r.Method == "GET":
		running, _ := runShellStatic("systemctl is-active sshd 2>/dev/null || systemctl is-active ssh 2>/dev/null")
		version, _ := runShellStatic("ssh -V 2>&1 | head -1")
		jsonOk(w, map[string]interface{}{"running": strings.TrimSpace(running) == "active", "version": version})
	case r.URL.Path == "/api/ssh/start" && r.Method == "POST":
		runShellStatic("sudo systemctl enable ssh sshd 2>/dev/null; sudo systemctl start sshd 2>/dev/null || sudo systemctl start ssh 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
	case r.URL.Path == "/api/ssh/stop" && r.Method == "POST":
		runShellStatic("sudo systemctl stop sshd ssh 2>/dev/null; sudo systemctl disable ssh sshd 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
	default:
		jsonError(w, 404, "Not found")
	}
}

// ═══════════════════════════════════
// FTP
// ═══════════════════════════════════

func handleFtpRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	switch {
	case r.URL.Path == "/api/ftp/status" && r.Method == "GET":
		_, installed := runShellStatic("which vsftpd 2>/dev/null || test -x /usr/sbin/vsftpd && echo yes")
		running1, _ := runSafe("systemctl", "is-active", "vsftpd")
		running := strings.TrimSpace(running1) == "active"
		jsonOk(w, map[string]interface{}{"installed": installed, "running": running})
	case r.URL.Path == "/api/ftp/start" && r.Method == "POST":
		runShellStatic("sudo systemctl enable vsftpd 2>/dev/null; sudo systemctl start vsftpd 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
	case r.URL.Path == "/api/ftp/stop" && r.Method == "POST":
		runShellStatic("sudo systemctl stop vsftpd 2>/dev/null; sudo systemctl disable vsftpd 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
	default:
		jsonError(w, 404, "Not found")
	}
}

// ═══════════════════════════════════
// NFS
// ═══════════════════════════════════

func handleNfsRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	switch {
	case r.URL.Path == "/api/nfs/status" && r.Method == "GET":
		_, installed := runShellStatic("dpkg -l nfs-kernel-server 2>/dev/null | grep -q '^ii' && echo yes")
		running1, _ := runSafe("systemctl", "is-active", "nfs-server")
		running := strings.TrimSpace(running1) == "active"
		exports := readFileStr("/etc/exports")
		jsonOk(w, map[string]interface{}{"installed": installed, "running": running, "exports": exports})
	case r.URL.Path == "/api/nfs/start" && r.Method == "POST":
		runShellStatic("sudo systemctl enable nfs-server 2>/dev/null; sudo systemctl start nfs-server 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
	case r.URL.Path == "/api/nfs/stop" && r.Method == "POST":
		runShellStatic("sudo systemctl stop nfs-server 2>/dev/null; sudo systemctl disable nfs-server 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
	default:
		jsonError(w, 404, "Not found")
	}
}

// ═══════════════════════════════════
// DNS
// ═══════════════════════════════════

func handleDnsRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	if r.URL.Path == "/api/dns/status" && r.Method == "GET" {
		servers := []string{}
		resolv := readFileStr("/etc/resolv.conf")
		for _, line := range strings.Split(resolv, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				parts := strings.Fields(line)
				if len(parts) >= 2 { servers = append(servers, parts[1]) }
			}
		}
		jsonOk(w, map[string]interface{}{"servers": servers})
		return
	}
	jsonError(w, 404, "Not found")
}

// ═══════════════════════════════════
// Certificates
// ═══════════════════════════════════

func handleCertsRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	urlPath := r.URL.Path
	method := r.Method

	if urlPath == "/api/certs/status" && method == "GET" {
		_, certbotInstalled := runSafe("which", "certbot")
		certs := []interface{}{}
		if certList, ok := runSafe("sudo", "certbot", "certificates"); ok {
			certs = parseCertbotCertificates(certList)
		}
		jsonOk(w, map[string]interface{}{"certbotInstalled": certbotInstalled, "certificates": certs})
		return
	}

	if urlPath == "/api/certs/request" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		domain := bodyStr(body, "domain")
		email := bodyStr(body, "email")
		certMethod := bodyStr(body, "method")
		if !isValidDomain(domain) { jsonError(w, 400, "Invalid domain"); return }
		if !isValidEmail(email) { jsonError(w, 400, "Invalid email"); return }
		args := []string{"certbot", "certonly", "--non-interactive", "--agree-tos", "-m", email}
		if certMethod == "standalone" {
			args = append(args, "--standalone", "-d", domain)
		} else {
			args = append(args, "--webroot", "-w", "/var/www/html", "-d", domain)
		}
		log, ok := runSafe("sudo", args...)
		if ok { jsonOk(w, map[string]interface{}{"ok": true, "log": log}) } else { jsonError(w, 500, "Certificate request failed") }
		return
	}

	if urlPath == "/api/certs/renew" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		domain := bodyStr(body, "domain")
		if !isValidDomain(domain) { jsonError(w, 400, "Invalid domain"); return }
		log, _ := runSafe("sudo", "certbot", "renew", "--cert-name", domain, "--force-renewal")
		jsonOk(w, map[string]interface{}{"ok": true, "log": log})
		return
	}

	if urlPath == "/api/certs/delete" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		domain := bodyStr(body, "domain")
		if !isValidDomain(domain) { jsonError(w, 400, "Invalid domain"); return }
		runSafe("sudo", "certbot", "delete", "--cert-name", domain, "--non-interactive")
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	jsonError(w, 404, "Not found")
}

// ═══════════════════════════════════
// Proxy (nginx reverse proxy rules)
// ═══════════════════════════════════

func handleProxyRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	urlPath := r.URL.Path
	method := r.Method

	if urlPath == "/api/proxy/status" && method == "GET" {
		_, installed := runSafe("which", "nginx")
		running1, _ := runSafe("systemctl", "is-active", "nginx")
		running := strings.TrimSpace(running1) == "active"
		var rules []interface{}
		if data, err := os.ReadFile(proxyConfigFile); err == nil { json.Unmarshal(data, &rules) }
		if rules == nil { rules = []interface{}{} }
		jsonOk(w, map[string]interface{}{"installed": installed, "running": running, "rules": rules})
		return
	}

	if urlPath == "/api/proxy/rules" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		rules, _ := body["rules"].([]interface{})
		writeJSONConfig(proxyConfigFile, rules)
		runShellStatic("sudo nginx -t 2>/dev/null && sudo systemctl reload nginx 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true})
		return
	}

	jsonError(w, 404, "Not found")
}

// ═══════════════════════════════════
// Portal (port config)
// ═══════════════════════════════════

func handlePortalRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	if r.URL.Path == "/api/portal/status" && r.Method == "GET" {
		jsonOk(w, map[string]interface{}{"httpPort": 5000, "httpsEnabled": false})
		return
	}
	if r.URL.Path == "/api/portal/config" && r.Method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		_ = body
		jsonOk(w, map[string]interface{}{"ok": true, "needsRestart": true})
		return
	}
	jsonError(w, 404, "Not found")
}

// ═══════════════════════════════════
// WebDAV
// ═══════════════════════════════════

func handleWebdavRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	urlPath := r.URL.Path
	method := r.Method

	if urlPath == "/api/webdav/status" && method == "GET" {
		_, installed := runSafe("which", "nginx")
		running := false
		if _, err := os.Stat("/etc/nginx/sites-enabled/nimos-webdav.conf"); err == nil { running = true }
		jsonOk(w, map[string]interface{}{"installed": installed, "running": running})
		return
	}
	if urlPath == "/api/webdav/start" && method == "POST" {
		runSafe("sudo", "ln", "-sf", "/etc/nginx/sites-available/nimos-webdav.conf", "/etc/nginx/sites-enabled/")
		runShellStatic("sudo nginx -t 2>/dev/null && sudo systemctl reload nginx 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}
	if urlPath == "/api/webdav/stop" && method == "POST" {
		runSafe("sudo", "rm", "-f", "/etc/nginx/sites-enabled/nimos-webdav.conf")
		runShellStatic("sudo nginx -t 2>/dev/null && sudo systemctl reload nginx 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}
	jsonError(w, 404, "Not found")
}

// ═══════════════════════════════════
// SMB / Samba
// ═══════════════════════════════════

func handleSmbRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	urlPath := r.URL.Path
	method := r.Method

	if urlPath == "/api/smb/status" && method == "GET" {
		_, installed := runShellStatic("which smbd 2>/dev/null || test -x /usr/sbin/smbd && echo yes")
		running1, _ := runSafe("systemctl", "is-active", "smbd")
		running := strings.TrimSpace(running1) == "active"
		version, _ := runSafe("smbd", "--version")
		config := readJSONConfig(smbConfigFile, map[string]interface{}{"workgroup": "WORKGROUP", "serverString": "NimOS NAS"})
		jsonOk(w, map[string]interface{}{"installed": installed, "running": running, "version": version, "config": config, "port": 445})
		return
	}

	if urlPath == "/api/smb/config" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		current := readJSONConfig(smbConfigFile, map[string]interface{}{})
		for k, v := range body { current[k] = v }
		writeJSONConfig(smbConfigFile, current)
		jsonOk(w, map[string]interface{}{"ok": true, "config": current}); return
	}

	if urlPath == "/api/smb/start" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		runShellStatic("sudo systemctl enable smbd nmbd 2>/dev/null; sudo systemctl start smbd nmbd 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}

	if urlPath == "/api/smb/stop" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		runShellStatic("sudo systemctl stop smbd nmbd 2>/dev/null; sudo systemctl disable smbd nmbd 2>/dev/null")
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}

	if urlPath == "/api/smb/restart" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		runSafe("sudo", "systemctl", "restart", "smbd", "nmbd")
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}

	if urlPath == "/api/smb/apply" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		runSafe("sudo", "smbcontrol", "all", "reload-config")
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}

	if urlPath == "/api/smb/set-password" && method == "POST" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		body, _ := readBody(r)
		username := bodyStr(body, "username")
		password := bodyStr(body, "password")
		if username == "" || password == "" { jsonError(w, 400, "Username and password required"); return }
		handleOp(Request{Op: "user.set_smb_password", Username: username, Password: password})
		jsonOk(w, map[string]interface{}{"ok": true}); return
	}

	// PUT /api/smb/share/:name
	reSmbShare := regexp.MustCompile(`^/api/smb/share/([a-zA-Z0-9_-]+)$`)
	if m := reSmbShare.FindStringSubmatch(urlPath); m != nil && method == "PUT" {
		if session.Role != "admin" { jsonError(w, 403, "Admin required"); return }
		// Toggle SMB on share — simplified, would need share update
		jsonOk(w, map[string]interface{}{"ok": true, "name": m[1]}); return
	}

	jsonError(w, 404, "Not found")
}

// ═══════════════════════════════════
// Firewall (GET endpoints)
// ═══════════════════════════════════

func handleFirewallRoutes(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil { return }
	urlPath := r.URL.Path

	if urlPath == "/api/firewall" || urlPath == "/api/firewall/scan" {
		jsonOk(w, getFirewallScanGo()); return
	}
	if urlPath == "/api/firewall/rules" {
		jsonOk(w, getFirewallRulesGo()); return
	}
	if urlPath == "/api/firewall/ports" {
		jsonOk(w, getListeningPortsGo()); return
	}

	// ── UPnP Router endpoints ──
	if urlPath == "/api/router/status" && r.Method == "GET" {
		jsonOk(w, getRouterStatus())
		return
	}
	if urlPath == "/api/router/ports" && r.Method == "GET" {
		jsonOk(w, getRouterPorts())
		return
	}
	if urlPath == "/api/router/port" && r.Method == "POST" {
		body, _ := readBody(r)
		jsonOk(w, addRouterPort(body))
		return
	}
	if urlPath == "/api/router/port" && r.Method == "DELETE" {
		body, _ := readBody(r)
		jsonOk(w, removeRouterPort(body))
		return
	}
	if urlPath == "/api/router/test" && r.Method == "POST" {
		body, _ := readBody(r)
		jsonOk(w, testRouterPort(body))
		return
	}

	jsonError(w, 404, "Not found")
}

func getFirewallRulesGo() map[string]interface{} {
	ufwOut, _ := runSafe("ufw", "status", "numbered")
	return map[string]interface{}{"rules": ufwOut, "active": strings.Contains(ufwOut, "Status: active")}
}

func getListeningPortsGo() map[string]interface{} {
	out, _ := runSafe("ss", "-tlnp")
	return map[string]interface{}{"ports": out}
}

func getFirewallScanGo() map[string]interface{} {
	rules := getFirewallRulesGo()
	ports := getListeningPortsGo()
	return map[string]interface{}{"firewall": rules, "listening": ports}
}

// ═══════════════════════════════════
// Register all network routes
// ═══════════════════════════════════

func registerNetworkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/ddns/", handleDdnsRoutes)
	mux.HandleFunc("/api/remote-access/", handleRemoteAccessRoutes)
	mux.HandleFunc("/api/ssh/", handleSshRoutes)
	mux.HandleFunc("/api/ftp/", handleFtpRoutes)
	mux.HandleFunc("/api/nfs/", handleNfsRoutes)
	mux.HandleFunc("/api/dns/", handleDnsRoutes)
	mux.HandleFunc("/api/certs/", handleCertsRoutes)
	mux.HandleFunc("/api/proxy/", handleProxyRoutes)
	mux.HandleFunc("/api/portal/", handlePortalRoutes)
	mux.HandleFunc("/api/webdav/", handleWebdavRoutes)
	mux.HandleFunc("/api/smb/", handleSmbRoutes)
	mux.HandleFunc("/api/firewall", handleFirewallRoutes)
	mux.HandleFunc("/api/firewall/", handleFirewallRoutes)
	mux.HandleFunc("/api/router/", handleFirewallRoutes)
	mux.HandleFunc("/api/vms/", handleVMsRoutes)
}

// ═══════════════════════════════════════════════════════════════════════════════
// UPnP Router — Port forwarding via miniupnpc
// ═══════════════════════════════════════════════════════════════════════════════

// getRouterStatus detects the router via UPnP and returns its info.
func getRouterStatus() map[string]interface{} {
	_, hasUpnpc := runSafe("which", "upnpc")
	if !hasUpnpc {
		return map[string]interface{}{
			"available": false,
			"error":     "miniupnpc not installed. Install with: apt install miniupnpc",
		}
	}

	out, ok := runSafe("upnpc", "-s")
	if !ok {
		return map[string]interface{}{
			"available": true,
			"detected":  false,
			"error":     "No UPnP router detected. Check if UPnP is enabled in your router settings.",
		}
	}

	result := map[string]interface{}{
		"available": true,
		"detected":  true,
		"raw":       out,
	}

	// Parse useful info from upnpc -s output
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Local LAN ip address") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				result["localIP"] = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(line, "ExternalIPAddress") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				result["externalIP"] = strings.TrimSpace(parts[1])
			}
		} else if strings.Contains(line, "desc:") {
			// Router description URL often contains model info
			result["desc"] = strings.TrimSpace(line)
		}
	}

	return result
}

// getRouterPorts lists current UPnP port forwardings on the router.
func getRouterPorts() map[string]interface{} {
	out, ok := runSafe("upnpc", "-l")
	if !ok {
		return map[string]interface{}{"error": "Cannot list port forwardings", "ports": []interface{}{}}
	}

	var ports []map[string]interface{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// Format: " N TCP  PORT->IP:PORT  'description' '' 0"
		if !strings.Contains(line, "->") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// Parse protocol and external port
		proto := ""
		extPort := ""
		intTarget := ""
		desc := ""

		for i, f := range fields {
			if f == "TCP" || f == "UDP" {
				proto = f
			}
			if strings.Contains(f, "->") {
				parts := strings.SplitN(f, "->", 2)
				extPort = parts[0]
				if len(parts) > 1 {
					intTarget = parts[1]
				}
			}
			if strings.HasPrefix(f, "'") && i > 0 {
				// Collect description between quotes
				desc = strings.Trim(strings.Join(fields[i:], " "), "' ")
				break
			}
		}

		if extPort != "" {
			ports = append(ports, map[string]interface{}{
				"protocol":    proto,
				"externalPort": extPort,
				"target":      intTarget,
				"description": desc,
			})
		}
	}

	if ports == nil {
		ports = []map[string]interface{}{}
	}
	return map[string]interface{}{"ports": ports}
}

// addRouterPort opens a port forwarding on the router via UPnP.
func addRouterPort(body map[string]interface{}) map[string]interface{} {
	portF, _ := body["port"].(float64)
	port := fmt.Sprintf("%d", int(portF))
	protocol := bodyStr(body, "protocol")
	description := bodyStr(body, "description")

	if port == "0" || port == "" {
		return map[string]interface{}{"error": "Port required"}
	}
	// Validate port is numeric
	if matched, _ := regexp.MatchString(`^\d{1,5}$`, port); !matched {
		return map[string]interface{}{"error": "Invalid port number"}
	}
	if protocol == "" {
		protocol = "TCP"
	}
	protocol = strings.ToUpper(protocol)
	if protocol != "TCP" && protocol != "UDP" {
		return map[string]interface{}{"error": "Protocol must be TCP or UDP"}
	}
	if description == "" {
		description = "NimOS"
	}

	// Get local IP for the mapping
	localIP := ""
	if lip, ok := runSafe("hostname", "-I"); ok {
		parts := strings.Fields(lip)
		if len(parts) > 0 {
			localIP = parts[0]
		}
	}
	if localIP == "" {
		return map[string]interface{}{"error": "Cannot determine local IP"}
	}

	// upnpc -a LOCAL_IP PORT PORT PROTOCOL [DURATION] [DESCRIPTION]
	out, ok := runSafe("upnpc", "-a", localIP, port, port, protocol, "0", description)
	if !ok {
		return map[string]interface{}{"error": "Failed to add port forwarding: " + out}
	}

	logMsg("UPnP: opened port %s/%s → %s:%s (%s)", port, protocol, localIP, port, description)
	return map[string]interface{}{"ok": true, "port": port, "protocol": protocol, "localIP": localIP}
}

// removeRouterPort removes a port forwarding from the router.
func removeRouterPort(body map[string]interface{}) map[string]interface{} {
	portF, _ := body["port"].(float64)
	port := fmt.Sprintf("%d", int(portF))
	protocol := bodyStr(body, "protocol")

	if port == "0" || port == "" {
		return map[string]interface{}{"error": "Port required"}
	}
	if protocol == "" {
		protocol = "TCP"
	}
	protocol = strings.ToUpper(protocol)

	out, ok := runSafe("upnpc", "-d", port, protocol)
	if !ok {
		return map[string]interface{}{"error": "Failed to remove port forwarding: " + out}
	}

	logMsg("UPnP: closed port %s/%s", port, protocol)
	return map[string]interface{}{"ok": true}
}

// testRouterPort tests if a port is reachable from outside using an external service.
func testRouterPort(body map[string]interface{}) map[string]interface{} {
	portF, _ := body["port"].(float64)
	port := fmt.Sprintf("%d", int(portF))

	if port == "0" || port == "" {
		return map[string]interface{}{"error": "Port required"}
	}

	// Get external IP
	extIP, _ := runSafe("curl", "-fsSL", "--connect-timeout", "5", "https://api.ipify.org")
	if extIP == "" {
		return map[string]interface{}{"error": "Cannot determine external IP"}
	}

	// Try to connect to ourselves from outside using a port check service
	checkURL := fmt.Sprintf("https://portchecker.co/check?port=%s&host=%s", port, extIP)
	out, ok := runSafe("curl", "-fsSL", "--connect-timeout", "10", checkURL)

	// Fallback: try simple TCP connect via our own external IP
	if !ok || out == "" {
		// Try direct TCP connect (works if no NAT hairpin issue)
		_, tcpOk := runSafe("bash", "-c", fmt.Sprintf("echo | timeout 5 nc -z %s %s 2>/dev/null", extIP, port))
		return map[string]interface{}{
			"externalIP": extIP,
			"port":       port,
			"reachable":  tcpOk,
			"method":     "tcp-direct",
		}
	}

	reachable := strings.Contains(strings.ToLower(out), "open") || strings.Contains(strings.ToLower(out), "reachable")
	return map[string]interface{}{
		"externalIP": extIP,
		"port":       port,
		"reachable":  reachable,
		"method":     "portchecker",
	}
}

// ═══════════════════════════════════════════════════════════════════
// Certbot output parser
// SEGURIDAD:
//   - No ejecuta comandos (solo parsea texto ya obtenido)
//   - No acepta input del usuario (parsea stdout de certbot)
//   - No expone private key path ni serial number (anti-fingerprinting)
//   - Todos los campos tienen límites estrictos de longitud
//   - Paths restringidos a /etc/letsencrypt/ por prefijo
// ═══════════════════════════════════════════════════════════════════

// parseCertbotCertificates parsea la salida de `certbot certificates`
// y devuelve una lista de certs con info estructurada.
//
// Formato esperado:
//   Certificate Name: example.com
//     Domains: example.com
//     Expiry Date: 2026-06-13 00:55:32+00:00 (VALID: 54 days)
//     Certificate Path: /etc/letsencrypt/live/example.com/fullchain.pem
//     Private Key Path: /etc/letsencrypt/live/example.com/privkey.pem
func parseCertbotCertificates(output string) []interface{} {
	certs := []interface{}{}
	if output == "" {
		return certs
	}

	blocks := strings.Split(output, "Certificate Name:")
	for i, block := range blocks {
		if i == 0 {
			continue
		}

		cert := map[string]interface{}{
			"valid":      false,
			"expiryDays": 0,
		}

		lines := strings.Split(block, "\n")

		if len(lines) > 0 {
			name := strings.TrimSpace(lines[0])
			if len(name) > 0 && len(name) <= 253 {
				cert["name"] = name
				cert["domain"] = name
			}
		}

		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)

			if strings.HasPrefix(trimmed, "Domains:") {
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Domains:"))
				if len(val) <= 1024 {
					cert["domains"] = strings.Fields(val)
				}
			} else if strings.HasPrefix(trimmed, "Expiry Date:") {
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Expiry Date:"))
				if len(val) <= 256 {
					cert["expiryRaw"] = val

					if len(val) >= 10 {
						cert["expiryDate"] = val[:10]
					}

					lower := strings.ToLower(val)
					if strings.Contains(lower, "(valid:") {
						cert["valid"] = true
						if idx := strings.Index(lower, "valid:"); idx >= 0 {
							rest := val[idx+6:]
							daysStr := ""
							for _, c := range rest {
								if c >= '0' && c <= '9' {
									daysStr += string(c)
								} else if len(daysStr) > 0 {
									break
								}
							}
							if daysStr != "" {
								var days int
								fmt.Sscanf(daysStr, "%d", &days)
								if days >= 0 && days < 10000 {
									cert["expiryDays"] = days
								}
							}
						}
					} else if strings.Contains(lower, "(invalid") || strings.Contains(lower, "expired") {
						cert["valid"] = false
					}
				}
			} else if strings.HasPrefix(trimmed, "Certificate Path:") {
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Certificate Path:"))
				if strings.HasPrefix(val, "/etc/letsencrypt/") && len(val) <= 512 {
					cert["certPath"] = val
				}
			} else if strings.HasPrefix(trimmed, "Private Key Path:") {
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Private Key Path:"))
				if strings.HasPrefix(val, "/etc/letsencrypt/") && len(val) <= 512 {
					cert["hasPrivateKey"] = true
					_ = val
				}
			} else if strings.HasPrefix(trimmed, "Key Type:") {
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Key Type:"))
				if len(val) <= 32 {
					cert["keyType"] = val
				}
			} else if strings.HasPrefix(trimmed, "Serial Number:") {
				cert["hasSerial"] = true
			}
		}

		if _, hasName := cert["name"]; hasName {
			certs = append(certs, cert)
		}
	}

	return certs
}
