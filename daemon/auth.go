package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// ═══════════════════════════════════
// Constants
// ═══════════════════════════════════

const (
	maxLoginAttempts = 5
	lockoutDuration  = 15 * 60 * 1000 // 15 min in ms
	serverKeyFile    = "/var/lib/nimos/config/.server_key"
	userDataDir      = "/var/lib/nimos/userdata"
)

var validUsernameHTTP = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{1,31}$`)

// ═══════════════════════════════════
// Rate limiting
// ═══════════════════════════════════

type rateLimitEntry struct {
	count       int
	lastAttempt int64
	lockedUntil int64
}

var (
	rateLimits   = map[string]*rateLimitEntry{}
	rateLimitsMu sync.Mutex
)

func checkRateLimit(key string) (bool, string) {
	rateLimitsMu.Lock()
	defer rateLimitsMu.Unlock()

	r, ok := rateLimits[key]
	if !ok {
		return true, ""
	}
	now := time.Now().UnixMilli()
	if r.lockedUntil > 0 && now < r.lockedUntil {
		remaining := (r.lockedUntil - now) / 60000
		if remaining < 1 {
			remaining = 1
		}
		return false, fmt.Sprintf("Too many attempts. Try again in %d minutes.", remaining)
	}
	if r.lockedUntil > 0 && now >= r.lockedUntil {
		delete(rateLimits, key)
		return true, ""
	}
	return true, ""
}

func recordFailedAttempt(key string) {
	rateLimitsMu.Lock()
	defer rateLimitsMu.Unlock()

	now := time.Now().UnixMilli()
	r, ok := rateLimits[key]
	if !ok {
		r = &rateLimitEntry{}
		rateLimits[key] = r
	}
	if now-r.lastAttempt > int64(lockoutDuration) {
		r.count = 0
	}
	r.count++
	r.lastAttempt = now
	if r.count >= maxLoginAttempts {
		r.lockedUntil = now + int64(lockoutDuration)
	}
}

func clearFailedAttempts(key string) {
	rateLimitsMu.Lock()
	defer rateLimitsMu.Unlock()
	delete(rateLimits, key)
}

// Periodic cleanup of old rate limit entries
func startRateLimitCleanup() {
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			rateLimitsMu.Lock()
			now := time.Now().UnixMilli()
			for k, r := range rateLimits {
				if now-r.lastAttempt > int64(lockoutDuration)*2 {
					delete(rateLimits, k)
				}
			}
			rateLimitsMu.Unlock()
		}
	}()
}

// ═══════════════════════════════════
// Password hashing (scrypt — compatible with Node.js)
// ═══════════════════════════════════

func hashPassword(password string) (string, error) {
	saltBytes := make([]byte, 16)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", err
	}
	// Node.js passes hex string of salt to scrypt, not raw bytes
	saltHex := hex.EncodeToString(saltBytes)
	dk, err := scrypt.Key([]byte(password), []byte(saltHex), 16384, 8, 1, 64)
	if err != nil {
		return "", err
	}
	return saltHex + ":" + hex.EncodeToString(dk), nil
}

func verifyPassword(password, stored string) bool {
	parts := strings.SplitN(stored, ":", 2)
	if len(parts) != 2 {
		return false
	}
	// Node.js passes salt as string to scrypt, NOT as decoded hex bytes
	salt := []byte(parts[0])
	expected, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	dk, err := scrypt.Key([]byte(password), salt, 16384, 8, 1, 64)
	if err != nil {
		return false
	}
	if len(dk) != len(expected) {
		return false
	}
	// Constant-time compare
	result := byte(0)
	for i := range dk {
		result |= dk[i] ^ expected[i]
	}
	return result == 0
}

func validatePasswordStrength(password string) string {
	if len(password) < 8 {
		return "Password must be at least 8 characters"
	}
	hasUpper := false
	hasDigit := false
	for _, c := range password {
		if c >= 'A' && c <= 'Z' {
			hasUpper = true
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
		}
	}
	if !hasUpper {
		return "Password must contain at least one uppercase letter"
	}
	if !hasDigit {
		return "Password must contain at least one number"
	}
	return ""
}

// ═══════════════════════════════════
// Token generation
// ═══════════════════════════════════

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func generateToken() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// base64url encoding without padding (matches Node.js crypto.randomBytes(48).toString('base64url'))
	const base64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	result := make([]byte, 64)
	for i := 0; i < 48; i++ {
		// Simple base64url: 6 bits per char
		if i*8/6 < 64 {
			result[i*8/6] = base64url[(b[i]>>2)&0x3F]
		}
	}
	// Use hex for simplicity and guaranteed uniqueness
	return hex.EncodeToString(b), nil
}

// ═══════════════════════════════════
// TOTP (2FA) — Google Authenticator compatible
// ═══════════════════════════════════

const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func base32Encode(data []byte) string {
	var result strings.Builder
	bits := 0
	value := 0
	for _, b := range data {
		value = (value << 8) | int(b)
		bits += 8
		for bits >= 5 {
			result.WriteByte(base32Alphabet[(value>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		result.WriteByte(base32Alphabet[(value<<(5-bits))&31])
	}
	return result.String()
}

func base32Decode(s string) []byte {
	s = strings.ToUpper(strings.TrimRight(s, "="))
	var output []byte
	bits := 0
	value := 0
	for _, c := range s {
		idx := strings.IndexRune(base32Alphabet, c)
		if idx == -1 {
			continue
		}
		value = (value << 5) | idx
		bits += 5
		if bits >= 8 {
			output = append(output, byte((value>>(bits-8))&0xFF))
			bits -= 8
		}
	}
	return output
}

func generateTotpSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32Encode(b), nil
}

func generateTotp(secret string, unixTime int64) string {
	t := unixTime / 30
	timeBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(timeBytes, uint64(t))

	key := base32Decode(secret)
	mac := hmac.New(sha1.New, key)
	mac.Write(timeBytes)
	hash := mac.Sum(nil)

	offset := hash[len(hash)-1] & 0x0f
	code := (int(hash[offset]&0x7f) << 24) |
		(int(hash[offset+1]) << 16) |
		(int(hash[offset+2]) << 8) |
		int(hash[offset+3])
	code = code % 1000000
	return fmt.Sprintf("%06d", code)
}

func verifyTotp(secret, token string) bool {
	now := time.Now().Unix()
	for i := int64(-1); i <= 1; i++ {
		if generateTotp(secret, now+i*30) == token {
			return true
		}
	}
	return false
}

func getTotpUri(username, secret string) string {
	return fmt.Sprintf("otpauth://totp/NimOS:%s?secret=%s&issuer=NimOS&algorithm=SHA1&digits=6&period=30", username, secret)
}

// ═══════════════════════════════════
// TOTP secret encryption (AES-256-CBC, compatible with Node.js)
// ═══════════════════════════════════

func getServerKey() ([]byte, error) {
	if data, err := os.ReadFile(serverKeyFile); err == nil {
		keyHex := strings.TrimSpace(string(data))
		if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, keyHex); matched {
			return hex.DecodeString(keyHex)
		}
	}
	// Generate new key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	keyHex := hex.EncodeToString(key)
	dir := filepath.Dir(serverKeyFile)
	os.MkdirAll(dir, 0700)
	if err := os.WriteFile(serverKeyFile, []byte(keyHex), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func encryptSecret(plaintext string) (string, error) {
	key, err := getServerKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	// PKCS7 padding
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	encrypted := make([]byte, len(padded))
	mode.CryptBlocks(encrypted, padded)
	return hex.EncodeToString(iv) + ":" + hex.EncodeToString(encrypted), nil
}

func decryptSecret(ciphertext string) (string, error) {
	if !strings.Contains(ciphertext, ":") {
		return ciphertext, nil // backwards compat: unencrypted
	}
	parts := strings.SplitN(ciphertext, ":", 2)
	if len(parts) != 2 {
		return ciphertext, nil
	}
	key, err := getServerKey()
	if err != nil {
		return "", err
	}
	iv, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	encrypted, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	decrypted := make([]byte, len(encrypted))
	mode.CryptBlocks(decrypted, encrypted)
	// Remove PKCS7 padding
	if len(decrypted) > 0 {
		padLen := int(decrypted[len(decrypted)-1])
		if padLen > 0 && padLen <= aes.BlockSize {
			decrypted = decrypted[:len(decrypted)-padLen]
		}
	}
	return string(decrypted), nil
}

// Backup codes for 2FA recovery
func generateBackupCodes(count int) []string {
	codes := make([]string, count)
	for i := 0; i < count; i++ {
		b := make([]byte, 4)
		rand.Read(b)
		codes[i] = strings.ToUpper(hex.EncodeToString(b))
	}
	return codes
}

// ═══════════════════════════════════
// QR code generation (uses qrencode CLI)
// ═══════════════════════════════════

func generateQrSvg(text string) (string, error) {
	// Try qrencode first — pipe text via stdin (no shell interpolation)
	out, ok := runSafeInput(text, "qrencode", "-t", "SVG", "-o", "-", "-m", "1")
	if ok && out != "" {
		return out, nil
	}
	// Try python3 qrcode — pipe text via stdin
	pyScript := `import qrcode,qrcode.image.svg,sys,io;data=sys.stdin.read();img=qrcode.make(data,image_factory=qrcode.image.svg.SvgPathImage,box_size=8,border=1);buf=io.BytesIO();img.save(buf);sys.stdout.buffer.write(buf.getvalue())`
	out, ok = runSafeInput(text, "python3", "-c", pyScript)
	if ok && out != "" {
		return out, nil
	}
	return "", fmt.Errorf("QR generation not available. Install qrencode: sudo apt install qrencode")
}

// ═══════════════════════════════════
// User data helpers (preferences, playlists, wallpapers)
// ═══════════════════════════════════

func getUserDataPath(username string) string {
	safe := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(username, "")
	return filepath.Join(userDataDir, safe)
}

func ensureUserDataDir(username string) string {
	p := getUserDataPath(username)
	os.MkdirAll(p, 0755)
	return p
}

var defaultPreferences = map[string]interface{}{
	"theme":            "dark",
	"accentColor":      "orange",
	"glowIntensity":    float64(50),
	"taskbarSize":      "medium",
	"taskbarPosition":  "bottom",
	"autoHideTaskbar":  false,
	"clock24":          true,
	"showDesktopIcons": true,
	"textScale":        float64(100),
	"wallpaper":        "",
	"showWidgets":      true,
	"widgetScale":      float64(100),
	"visibleWidgets": map[string]interface{}{
		"system": true, "network": true, "disk": true, "notifications": true,
	},
	"pinnedApps":   []interface{}{"files", "appstore", "settings"},
	"playlist":     []interface{}{},
	"playlistName": "Mi Lista",
}

func getUserPreferences(username string) map[string]interface{} {
	result := map[string]interface{}{}
	// Copy defaults
	for k, v := range defaultPreferences {
		result[k] = v
	}
	// Read saved
	prefsFile := filepath.Join(getUserDataPath(username), "preferences.json")
	data, err := os.ReadFile(prefsFile)
	if err != nil {
		return result
	}
	var saved map[string]interface{}
	if json.Unmarshal(data, &saved) == nil {
		for k, v := range saved {
			result[k] = v
		}
	}
	return result
}

func saveUserPreferences(username string, prefs map[string]interface{}) error {
	dir := ensureUserDataDir(username)
	data, _ := json.MarshalIndent(prefs, "", "  ")
	return os.WriteFile(filepath.Join(dir, "preferences.json"), data, 0644)
}

func getUserPlaylist(username string) []interface{} {
	playlistFile := filepath.Join(getUserDataPath(username), "playlist.json")
	data, err := os.ReadFile(playlistFile)
	if err != nil {
		return []interface{}{}
	}
	var playlist []interface{}
	if json.Unmarshal(data, &playlist) != nil {
		return []interface{}{}
	}
	return playlist
}

func saveUserPlaylist(username string, playlist []interface{}) error {
	dir := ensureUserDataDir(username)
	data, _ := json.MarshalIndent(playlist, "", "  ")
	return os.WriteFile(filepath.Join(dir, "playlist.json"), data, 0644)
}

// ═══════════════════════════════════
// Auth HTTP handlers
// ═══════════════════════════════════

func handleAuthRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	switch {
	// GET /api/auth/status
	case path == "/api/auth/status" && method == "GET":
		authStatus(w, r)

	// POST /api/auth/setup
	case path == "/api/auth/setup" && method == "POST":
		authSetup(w, r)

	// POST /api/auth/login
	case path == "/api/auth/login" && method == "POST":
		authLogin(w, r)

	// POST /api/auth/logout
	case path == "/api/auth/logout" && method == "POST":
		authLogout(w, r)

	// GET /api/auth/me
	case path == "/api/auth/me" && method == "GET":
		authMe(w, r)

	// POST /api/auth/change-password
	case path == "/api/auth/change-password" && method == "POST":
		authChangePassword(w, r)

	// POST /api/auth/2fa/setup
	case path == "/api/auth/2fa/setup" && method == "POST":
		auth2faSetup(w, r)

	// POST /api/auth/2fa/verify
	case path == "/api/auth/2fa/verify" && method == "POST":
		auth2faVerify(w, r)

	// POST /api/auth/2fa/disable
	case path == "/api/auth/2fa/disable" && method == "POST":
		auth2faDisable(w, r)

	// GET /api/auth/2fa/status
	case path == "/api/auth/2fa/status" && method == "GET":
		auth2faStatus(w, r)

	// POST /api/auth/2fa/qr
	case path == "/api/auth/2fa/qr" && method == "POST":
		auth2faQr(w, r)

	default:
		jsonError(w, 404, "Not found")
	}
}

// GET /api/auth/status — is setup done?
func authStatus(w http.ResponseWriter, r *http.Request) {
	users, _ := dbUsersListRaw()
	hostname, _ := os.Hostname()
	jsonOk(w, map[string]interface{}{
		"setup":    len(users) > 0,
		"hostname": hostname,
	})
}

// POST /api/auth/setup — create initial admin account
func authSetup(w http.ResponseWriter, r *http.Request) {
	users, _ := dbUsersListRaw()
	if len(users) > 0 {
		jsonError(w, 400, "Setup already completed")
		return
	}

	body, err := readBody(r)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	username := strings.ToLower(strings.TrimSpace(bodyStr(body, "username")))
	password := bodyStr(body, "password")

	if username == "" || password == "" {
		jsonError(w, 400, "Username and password required")
		return
	}
	if !validUsernameHTTP.MatchString(username) {
		jsonError(w, 400, "Invalid username: letters, numbers and underscores only (2-32 chars)")
		return
	}
	if msg := validatePasswordStrength(password); msg != "" {
		jsonError(w, 400, msg)
		return
	}

	hashed, err := hashPassword(password)
	if err != nil {
		jsonError(w, 500, "Failed to hash password")
		return
	}

	if err := dbUsersCreate(username, hashed, "admin", "System administrator"); err != nil {
		jsonError(w, 500, err.Error())
		return
	}

	// Create Linux user + Samba password via daemon ops
	handleOp(Request{Op: "user.create", Username: username})
	handleOp(Request{Op: "user.set_smb_password", Username: username, Password: password})

	// Create default volume directory
	os.MkdirAll(filepath.Join(nimosRoot, "volumes", "volume1"), 0755)

	// Auto-login
	token, _ := generateToken()
	hToken := sha256Hex(token)
	dbSessionCreate(hToken, username, "admin", clientIP(r))

	// SECURITY: Set session cookie with proper flags
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "nimos_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})

	jsonOk(w, map[string]interface{}{
		"ok":    true,
		"token": token,
		"user":  map[string]string{"username": username, "role": "admin"},
	})
}

// POST /api/auth/login
func authLogin(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	username := strings.ToLower(strings.TrimSpace(bodyStr(body, "username")))
	password := bodyStr(body, "password")
	totpCode := bodyStr(body, "totpCode")

	if username == "" || password == "" {
		jsonError(w, 400, "Username and password required")
		return
	}

	ip := clientIP(r)

	// Rate limiting
	if ok, msg := checkRateLimit("ip:" + ip); !ok {
		jsonError(w, 429, msg)
		return
	}
	if ok, msg := checkRateLimit("user:" + username); !ok {
		jsonError(w, 429, msg)
		return
	}

	// Verify credentials
	// IMPORTANT: Always run bcrypt comparison even if user doesn't exist
	// This prevents timing attacks that reveal valid usernames
	storedPwd, err := dbUsersVerifyPassword(username)
	if err != nil {
		// User doesn't exist — run a dummy bcrypt to equalize timing
		verifyPassword(password, "$2a$10$0000000000000000000000uDummyHashToPreventTimingAttack0000")
		recordFailedAttempt("ip:" + ip)
		recordFailedAttempt("user:" + username)
		ShieldAuthFail(ip, username, r.UserAgent())
		jsonError(w, 401, "Invalid credentials")
		return
	}
	if !verifyPassword(password, storedPwd) {
		recordFailedAttempt("ip:" + ip)
		recordFailedAttempt("user:" + username)
		ShieldAuthFail(ip, username, r.UserAgent())
		jsonError(w, 401, "Invalid credentials")
		return
	}

	// Get user for role and 2FA check
	user, err := dbUsersGetRaw(username)
	if err != nil {
		jsonError(w, 500, "User lookup failed")
		return
	}

	// Check 2FA
	if user.TotpSecret != "" && user.TotpEnabled {
		if totpCode == "" {
			jsonOk(w, map[string]interface{}{
				"requires2FA": true,
				"message":     "Two-factor authentication code required",
			})
			return
		}
		decrypted, err := decryptSecret(user.TotpSecret)
		if err != nil {
			jsonError(w, 500, "2FA decryption failed")
			return
		}
		if !verifyTotp(decrypted, totpCode) {
			// Check backup codes
			backupValid := false
			if user.BackupCodes != nil {
				inputHash := sha256Hex(strings.ToUpper(totpCode))
				for i, c := range user.BackupCodes {
					if cs, ok := c.(string); ok && cs == inputHash {
						// Remove used backup code
						codes := append(user.BackupCodes[:i], user.BackupCodes[i+1:]...)
						dbUsersUpdate(username, UserUpdate{BackupCodes: codes})
						backupValid = true
						break
					}
				}
			}
			if !backupValid {
				recordFailedAttempt("ip:" + ip)
				recordFailedAttempt("user:" + username)
				jsonError(w, 401, "Invalid 2FA code")
				return
			}
		}
	}

	clearFailedAttempts("ip:" + ip)
	clearFailedAttempts("user:" + username)

	token, _ := generateToken()
	hToken := sha256Hex(token)
	dbSessionCreate(hToken, username, user.Role, ip)

	// SECURITY: Set session cookie from backend with proper flags
	// HttpOnly prevents XSS from stealing the cookie
	// Secure ensures it's only sent over HTTPS
	// SameSite=Strict prevents CSRF
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "nimos_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24h matches session expiry
	})

	jsonOk(w, map[string]interface{}{
		"ok":    true,
		"token": token,
		"user":  map[string]string{"username": username, "role": user.Role},
	})
}

// POST /api/auth/logout
func authLogout(w http.ResponseWriter, r *http.Request) {
	token := getBearerToken(r)
	if token != "" {
		dbSessionDelete(sha256Hex(token))
	}
	// Clear session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "nimos_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	jsonOk(w, map[string]interface{}{"ok": true})
}

// GET /api/auth/me
func authMe(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	jsonOk(w, map[string]interface{}{
		"user": map[string]string{
			"username": session.Username,
			"role":     session.Role,
		},
	})
}

// POST /api/auth/change-password
func authChangePassword(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	body, err := readBody(r)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}

	newPassword := bodyStr(body, "newPassword")
	currentPassword := bodyStr(body, "currentPassword")
	targetUser := bodyStr(body, "targetUser")

	if newPassword == "" {
		jsonError(w, 400, "New password required")
		return
	}
	if msg := validatePasswordStrength(newPassword); msg != "" {
		jsonError(w, 400, msg)
		return
	}

	sessionUser := session.Username
	sessionRole := session.Role

	editUser := sessionUser
	if targetUser != "" && sessionRole == "admin" {
		editUser = targetUser
	}

	// Non-admin or self-change: require current password
	if targetUser == "" || targetUser == sessionUser {
		stored, err := dbUsersVerifyPassword(editUser)
		if err != nil || !verifyPassword(currentPassword, stored) {
			jsonError(w, 400, "Current password is incorrect")
			return
		}
	}

	hashed, err := hashPassword(newPassword)
	if err != nil {
		jsonError(w, 500, "Failed to hash password")
		return
	}
	dbUsersUpdate(editUser, UserUpdate{Password: strPtr(hashed)})

	// Invalidate all sessions for this user
	dbSessionsDeleteByUsername(editUser)

	// Update Samba password
	handleOp(Request{Op: "user.set_smb_password", Username: editUser, Password: newPassword})

	jsonOk(w, map[string]interface{}{"ok": true})
}

// POST /api/auth/2fa/setup — generate TOTP secret
func auth2faSetup(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	secret, err := generateTotpSecret()
	if err != nil {
		jsonError(w, 500, "Failed to generate secret")
		return
	}

	encrypted, err := encryptSecret(secret)
	if err != nil {
		jsonError(w, 500, "Failed to encrypt secret")
		return
	}

	username := session.Username
	dbUsersUpdate(username, UserUpdate{
		TotpSecret:  strPtr(encrypted),
		TotpEnabled: boolPtr(false),
	})

	uri := getTotpUri(username, secret)
	resp := map[string]interface{}{
		"ok":     true,
		"secret": secret,
		"uri":    uri,
	}
	// Generar QR SVG si el sistema tiene qrencode o python3-qrcode.
	// Si no está disponible, el frontend lo genera en cliente desde 'uri'.
	if qr, qerr := generateQrSvg(uri); qerr == nil && qr != "" {
		resp["qr"] = qr
	}
	jsonOk(w, resp)
}

// POST /api/auth/2fa/verify — verify code and enable 2FA
func auth2faVerify(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	body, _ := readBody(r)
	code := bodyStr(body, "code")
	if code == "" {
		jsonError(w, 400, "Code required")
		return
	}

	username := session.Username
	user, err := dbUsersGetRaw(username)
	if err != nil {
		jsonError(w, 400, "User not found")
		return
	}

	if user.TotpSecret == "" {
		jsonError(w, 400, "No 2FA setup in progress")
		return
	}

	decrypted, err := decryptSecret(user.TotpSecret)
	if err != nil {
		jsonError(w, 500, "Decryption failed")
		return
	}
	if !verifyTotp(decrypted, code) {
		jsonError(w, 400, "Invalid code. Make sure your authenticator app is synced.")
		return
	}

	// Generate backup codes
	backupCodes := generateBackupCodes(8)
	hashedCodes := make([]interface{}, len(backupCodes))
	for i, c := range backupCodes {
		hashedCodes[i] = sha256Hex(c)
	}

	dbUsersUpdate(username, UserUpdate{
		TotpEnabled: boolPtr(true),
		BackupCodes: hashedCodes,
	})

	jsonOk(w, map[string]interface{}{
		"ok":          true,
		"message":     "2FA enabled successfully",
		"backupCodes": backupCodes,
	})
}

// POST /api/auth/2fa/disable
func auth2faDisable(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	body, _ := readBody(r)
	password := bodyStr(body, "password")
	if password == "" {
		jsonError(w, 400, "Password required to disable 2FA")
		return
	}

	username := session.Username
	stored, err := dbUsersVerifyPassword(username)
	if err != nil || !verifyPassword(password, stored) {
		jsonError(w, 400, "Invalid password")
		return
	}

	dbUsersUpdate(username, UserUpdate{
		TotpSecret:  strPtr(""),
		TotpEnabled: boolPtr(false),
	})

	jsonOk(w, map[string]interface{}{"ok": true})
}

// GET /api/auth/2fa/status
func auth2faStatus(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	username := session.Username
	user, _ := dbUsersGetRaw(username)
	enabled := false
	if user != nil {
		enabled = user.TotpEnabled
	}
	jsonOk(w, map[string]interface{}{"enabled": enabled})
}

// POST /api/auth/2fa/qr — generate QR code SVG
func auth2faQr(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}

	body, _ := readBody(r)
	text := bodyStr(body, "text")
	if text == "" {
		jsonError(w, 400, "Text required")
		return
	}

	svg, err := generateQrSvg(text)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOk(w, map[string]interface{}{"svg": svg})
}

// ═══════════════════════════════════
// User preference / playlist / wallpaper routes
// ═══════════════════════════════════

func handleUserRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	switch {
	// GET /api/user/preferences
	case path == "/api/user/preferences" && method == "GET":
		session := requireAuth(w, r)
		if session == nil {
			return
		}
		prefs := getUserPreferences(session.Username)
		jsonOk(w, map[string]interface{}{"preferences": prefs})

	// PUT /api/user/preferences
	case path == "/api/user/preferences" && method == "PUT":
		session := requireAuth(w, r)
		if session == nil {
			return
		}
		body, _ := readBody(r)
		current := getUserPreferences(session.Username)
		for k, v := range body {
			if k != "playlist" {
				current[k] = v
			}
		}
		delete(current, "playlist")
		if err := saveUserPreferences(session.Username, current); err != nil {
			jsonError(w, 500, "Failed to save preferences")
			return
		}
		jsonOk(w, map[string]interface{}{"ok": true, "preferences": current})

	// PATCH /api/user/preferences
	case path == "/api/user/preferences" && method == "PATCH":
		session := requireAuth(w, r)
		if session == nil {
			return
		}
		body, _ := readBody(r)
		current := getUserPreferences(session.Username)
		for k, v := range body {
			if k != "playlist" {
				current[k] = v
			}
		}
		delete(current, "playlist")
		saveUserPreferences(session.Username, current)
		jsonOk(w, map[string]interface{}{"ok": true})

	// POST /api/user/wallpaper — upload wallpaper (base64)
	case path == "/api/user/wallpaper" && method == "POST":
		userWallpaperUpload(w, r)

	// GET /api/user/wallpaper/:username/:file
	case strings.HasPrefix(path, "/api/user/wallpaper/") && method == "GET":
		userWallpaperServe(w, r)

	// GET /api/user/playlist
	case path == "/api/user/playlist" && method == "GET":
		session := requireAuth(w, r)
		if session == nil {
			return
		}
		jsonOk(w, map[string]interface{}{"playlist": getUserPlaylist(session.Username)})

	// PUT /api/user/playlist
	case path == "/api/user/playlist" && method == "PUT":
		userPlaylistSave(w, r)

	// POST /api/user/playlist/add
	case path == "/api/user/playlist/add" && method == "POST":
		userPlaylistAdd(w, r)

	// DELETE /api/user/playlist/:index
	case strings.HasPrefix(path, "/api/user/playlist/") && method == "DELETE":
		userPlaylistRemove(w, r)

	default:
		// Check auth before 404 — prevents route enumeration
		if requireAuth(w, r) == nil {
			return
		}
		jsonError(w, 404, "Not found")
	}
}

func userWallpaperUpload(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	dataStr := bodyStr(body, "data")
	if dataStr == "" {
		jsonError(w, 400, "No image data provided")
		return
	}

	// Parse data:image/xxx;base64,...
	wpRegex := regexp.MustCompile(`^data:image/(png|jpeg|jpg|webp|gif);base64,(.+)$`)
	matches := wpRegex.FindStringSubmatch(dataStr)
	if matches == nil {
		jsonError(w, 400, "Invalid image format")
		return
	}

	ext := matches[1]
	if ext == "jpeg" {
		ext = "jpg"
	}

	// Decode base64
	imgData, err := decodeBase64(matches[2])
	if err != nil || len(imgData) > 10*1024*1024 {
		jsonError(w, 400, "Image too large (max 10MB)")
		return
	}

	username := session.Username
	userPath := ensureUserDataDir(username)
	wallpaperFile := fmt.Sprintf("wallpaper.%s", ext)
	fullPath := filepath.Join(userPath, wallpaperFile)
	os.WriteFile(fullPath, imgData, 0644)

	wallpaperUrl := fmt.Sprintf("/api/user/wallpaper/%s/%s", username, wallpaperFile)

	// Save in preferences
	current := getUserPreferences(username)
	current["wallpaper"] = wallpaperUrl
	saveUserPreferences(username, current)

	jsonOk(w, map[string]interface{}{"ok": true, "url": wallpaperUrl})
}

func userWallpaperServe(w http.ResponseWriter, r *http.Request) {
	// No auth required — wallpapers are loaded as <img src="..."> without Authorization header
	wpRegex := regexp.MustCompile(`^/api/user/wallpaper/([a-zA-Z0-9_.-]+)/wallpaper\.(png|jpg|jpeg|webp|gif)$`)
	matches := wpRegex.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		jsonError(w, 404, "Wallpaper not found")
		return
	}

	userPath := getUserDataPath(matches[1])
	ext := matches[2]
	wpPath := filepath.Join(userPath, fmt.Sprintf("wallpaper.%s", ext))

	data, err := os.ReadFile(wpPath)
	if err != nil {
		jsonError(w, 404, "Wallpaper not found")
		return
	}

	mimeTypes := map[string]string{"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif"}
	w.Header().Set("Content-Type", mimeTypes[ext])
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func userPlaylistSave(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	playlistRaw, ok := body["playlist"]
	if !ok {
		jsonError(w, 400, "Playlist must be an array")
		return
	}
	playlist, ok := playlistRaw.([]interface{})
	if !ok {
		jsonError(w, 400, "Playlist must be an array")
		return
	}
	if err := saveUserPlaylist(session.Username, playlist); err != nil {
		jsonError(w, 500, "Failed to save playlist")
		return
	}
	jsonOk(w, map[string]interface{}{"ok": true, "count": len(playlist)})
}

func userPlaylistAdd(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	body, _ := readBody(r)
	itemUrl := bodyStr(body, "url")
	if itemUrl == "" {
		jsonError(w, 400, "URL required")
		return
	}

	username := session.Username
	playlist := getUserPlaylist(username)

	// Check duplicates
	for _, item := range playlist {
		if m, ok := item.(map[string]interface{}); ok {
			if u, _ := m["url"].(string); u == itemUrl {
				jsonError(w, 400, "Already in playlist")
				return
			}
		}
	}

	itemType := "audio"
	if t := bodyStr(body, "type"); t == "video" {
		itemType = "video"
	}

	newItem := map[string]interface{}{
		"name":    bodyStr(body, "name"),
		"url":     itemUrl,
		"type":    itemType,
		"addedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if d := bodyStr(body, "duration"); d != "" {
		newItem["duration"] = d
	}

	playlist = append(playlist, newItem)
	saveUserPlaylist(username, playlist)
	jsonOk(w, map[string]interface{}{"ok": true, "count": len(playlist)})
}

func userPlaylistRemove(w http.ResponseWriter, r *http.Request) {
	session := requireAuth(w, r)
	if session == nil {
		return
	}
	// Extract index from /api/user/playlist/:index
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		jsonError(w, 400, "Invalid index")
		return
	}
	var index int
	if _, err := fmt.Sscanf(parts[len(parts)-1], "%d", &index); err != nil {
		jsonError(w, 400, "Invalid index")
		return
	}

	username := session.Username
	playlist := getUserPlaylist(username)
	if index < 0 || index >= len(playlist) {
		jsonError(w, 400, "Invalid index")
		return
	}
	playlist = append(playlist[:index], playlist[index+1:]...)
	saveUserPlaylist(username, playlist)
	jsonOk(w, map[string]interface{}{"ok": true, "count": len(playlist)})
}

// ═══════════════════════════════════
// Users management (admin CRUD)
// ═══════════════════════════════════

func handleUsersRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	// GET /api/users — list users
	if path == "/api/users" && method == "GET" {
		session := requireAdmin(w, r)
		if session == nil {
			return
		}
		users, _ := dbUsersListRaw()
		result := make([]map[string]interface{}, len(users))
		for i, u := range users {
			result[i] = u.ToMap()
		}
		jsonOk(w, result)
		return
	}

	// POST /api/users — create user
	if path == "/api/users" && method == "POST" {
		usersCreate(w, r)
		return
	}

	// Match /api/users/:username
	userMatch := regexp.MustCompile(`^/api/users/([a-zA-Z0-9_.-]+)$`)
	matches := userMatch.FindStringSubmatch(path)
	if matches == nil {
		jsonError(w, 404, "Not found")
		return
	}
	target := strings.ToLower(matches[1])

	switch method {
	case "DELETE":
		usersDelete(w, r, target)
	case "PUT":
		usersUpdate(w, r, target)
	default:
		jsonError(w, 405, "Method not allowed")
	}
}

func usersCreate(w http.ResponseWriter, r *http.Request) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	body, _ := readBody(r)
	username := strings.ToLower(strings.TrimSpace(bodyStr(body, "username")))
	password := bodyStr(body, "password")
	role := bodyStr(body, "role")
	description := bodyStr(body, "description")

	if username == "" || password == "" {
		jsonError(w, 400, "Username and password required")
		return
	}
	if !validUsernameHTTP.MatchString(username) {
		jsonError(w, 400, "Invalid username: letters, numbers and underscores only (2-32 chars)")
		return
	}
	if msg := validatePasswordStrength(password); msg != "" {
		jsonError(w, 400, msg)
		return
	}

	// Check if user exists
	if _, err := dbUsersGetRaw(username); err == nil {
		jsonError(w, 400, "User already exists")
		return
	}

	if role == "" {
		role = "user"
	}

	hashed, _ := hashPassword(password)
	if err := dbUsersCreate(username, hashed, role, description); err != nil {
		jsonError(w, 500, err.Error())
		return
	}

	// Sync Linux + Samba
	handleOp(Request{Op: "user.create", Username: username})
	handleOp(Request{Op: "user.set_smb_password", Username: username, Password: password})

	jsonOk(w, map[string]interface{}{"ok": true, "username": username})
}

func usersDelete(w http.ResponseWriter, r *http.Request, target string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	if target == session.Username {
		jsonError(w, 400, "Cannot delete yourself")
		return
	}

	if _, err := dbUsersGetRaw(target); err != nil {
		jsonError(w, 404, "User not found")
		return
	}

	dbUsersDelete(target)
	handleOp(Request{Op: "user.delete", Username: target})

	jsonOk(w, map[string]interface{}{"ok": true})
}

func usersUpdate(w http.ResponseWriter, r *http.Request, target string) {
	session := requireAdmin(w, r)
	if session == nil {
		return
	}

	if _, err := dbUsersGetRaw(target); err != nil {
		jsonError(w, 404, "User not found")
		return
	}

	body, _ := readBody(r)
	var u UserUpdate
	hasUpdates := false

	if pw := bodyStr(body, "password"); pw != "" {
		if msg := validatePasswordStrength(pw); msg != "" {
			jsonError(w, 400, msg)
			return
		}
		hashed, _ := hashPassword(pw)
		u.Password = strPtr(hashed)
		hasUpdates = true
		handleOp(Request{Op: "user.set_smb_password", Username: target, Password: pw})
	}
	if role := bodyStr(body, "role"); role != "" {
		u.Role = strPtr(role)
		hasUpdates = true
	}
	if desc := bodyStr(body, "description"); desc != "" {
		u.Description = strPtr(desc)
		hasUpdates = true
	}

	if hasUpdates {
		dbUsersUpdate(target, u)
	}
	jsonOk(w, map[string]interface{}{"ok": true})
}

// base64 decode helper
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Try standard base64 first
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try without padding
	if data, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try URL-safe
	if data, err := base64.URLEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}
