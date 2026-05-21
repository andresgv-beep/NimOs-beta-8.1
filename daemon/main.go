// NimOS — Permissions Daemon (nimos-daemon)
//
// Runs as root. Listens on Unix socket only.
// Accepts a closed catalog of operations — nothing else.
// Enforces permissions at the filesystem level (groups + ACLs).
//
// Socket: /run/nimos-daemon.sock
// Build:  go build -o nimos-daemon main.go

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ═══════════════════════════════════
// Configuration
// ═══════════════════════════════════

const (
	socketPath  = "/run/nimos-daemon.sock"
	maxReqSize  = 65536
	execTimeout = 10 * time.Second
	maxRetries  = 3
)

var (
	sharesFile  = getEnv("NIMOS_SHARES_FILE", "/var/lib/nimos/config/shares.json")
	usersFile   = getEnv("NIMOS_USERS_FILE", "/var/lib/nimos/config/users.json")
	serviceUser = getEnv("NIMOS_USER", "nimos")
	poolBase    = "/nimos/pools/"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ═══════════════════════════════════
// Logging
// ═══════════════════════════════════

func logMsg(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[nimos-daemon] %s %s", time.Now().UTC().Format(time.RFC3339Nano)[:23]+"Z", msg)
}

// ═══════════════════════════════════
// Input validation
// ═══════════════════════════════════

var (
	validShareName = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,63}$`)
	validUsername   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	systemUsers    = map[string]bool{
		"root": true, "daemon": true, "nobody": true, "www-data": true,
		"sshd": true, "nimos": true, "systemd-network": true,
		"systemd-resolve": true, "systemd-timesync": true,
		"messagebus": true, "syslog": true, "uuidd": true,
		"_apt": true, "avahi": true,
	}
)

func checkShareName(name string) error {
	if name == "" {
		return fmt.Errorf("shareName required")
	}
	if !validShareName.MatchString(name) {
		return fmt.Errorf("invalid shareName: %s", name)
	}
	return nil
}

func checkUsername(username string) error {
	if username == "" {
		return fmt.Errorf("username required")
	}
	if !validUsername.MatchString(username) {
		return fmt.Errorf("invalid username: %s", username)
	}
	if systemUsers[username] {
		return fmt.Errorf("rejected system username: %s", username)
	}
	return nil
}

func checkPoolPath(poolPath string) error {
	if poolPath == "" {
		return fmt.Errorf("poolPath required")
	}
	if !strings.HasPrefix(poolPath, poolBase) && !strings.HasPrefix(poolPath, "/nimos/") {
		return fmt.Errorf("invalid poolPath: must be within %s", poolBase)
	}
	if strings.Contains(poolPath, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	if _, err := os.Stat(poolPath); os.IsNotExist(err) {
		return fmt.Errorf("poolPath does not exist: %s", poolPath)
	}
	if !isPathOnMountedPool(poolPath) {
		return fmt.Errorf("pool not mounted at %s — refusing operation", poolPath)
	}
	return nil
}

func checkUid(uid interface{}) (int, error) {
	var n int
	switch v := uid.(type) {
	case float64:
		n = int(v)
	case string:
		var err error
		n, err = strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("invalid UID: %v", uid)
		}
	default:
		return 0, fmt.Errorf("invalid UID type: %v", uid)
	}
	if n < 1000 || n > 65000 {
		return 0, fmt.Errorf("invalid UID: %d (must be 1000-65000)", n)
	}
	return n, nil
}

func checkPermission(perm string) error {
	if perm != "ro" && perm != "rw" {
		return fmt.Errorf("invalid permission: %s (must be ro or rw)", perm)
	}
	return nil
}

// ═══════════════════════════════════
// Helper: safe command execution with retry
// ═══════════════════════════════════

// runShellStatic executes a STATIC command via shell (sh -c) with retry.
//
// SECURITY: This function rejects any command containing format verbs (%s, %d)
// or string concatenation markers (+) to prevent accidental interpolation.
// All ~46 callers use ONLY hardcoded string literals or pre-validated internal vars.
// User input MUST go through runSafe() / runSafeInput() / runCmd().
func runShellStatic(command string) (string, bool) {
	// Guard: reject commands that look like they contain interpolation
	if strings.Contains(command, "%s") || strings.Contains(command, "%d") || strings.Contains(command, "%v") {
		logMsg("SECURITY: runShellStatic rejected interpolated command: %s", command)
		return "", false
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx := exec.Command("sh", "-c", command)
		out, err := ctx.CombinedOutput()
		result := strings.TrimSpace(string(out))

		if err == nil {
			return result, true
		}

		// Retry on lock contention
		if strings.Contains(result, "bloquear") || strings.Contains(result, "lock") || strings.Contains(result, "unable to lock") {
			logMsg("exec retry (%d/%d): %s", attempt+1, maxRetries, command)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		logMsg("exec failed: %s → %s", command, result)
		return result, false
	}
	logMsg("exec gave up after %d retries: %s", maxRetries, command)
	return "", false
}

// runSafe executes a command with arguments directly (no shell).
// Same return signature as runShellStatic() for easy migration.
// ALWAYS prefer this over runShellStatic() when arguments contain user data.
func runSafe(cmd string, args ...string) (string, bool) {
	c := exec.Command(cmd, args...)
	out, err := c.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		logMsg("exec failed: %s %v → %s", cmd, args, result)
		return result, false
	}
	return result, true
}

// runSafeInput executes a command with data piped to stdin (no shell).
func runSafeInput(stdin string, cmd string, args ...string) (string, bool) {
	c := exec.Command(cmd, args...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		logMsg("exec failed: %s %v → %s", cmd, args, result)
		return result, false
	}
	return result, true
}

// ═══════════════════════════════════
// Input validation helpers
// ═══════════════════════════════════

var (
	reAlphanumDash    = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	reDomain          = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)
	reEmail           = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	reSnapshotName    = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
	reZfsDatasetSnap  = regexp.MustCompile(`^[a-zA-Z0-9_-]+(/[a-zA-Z0-9._-]+)*@[a-zA-Z0-9._-]{1,128}$`)
	reDevName         = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	reUnixUser        = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	reContainerId     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	reWgInterface     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`)
	reAbsPath         = regexp.MustCompile(`^/[a-zA-Z0-9/._ -]+$`)
)

func isValidDomain(s string) bool   { return reDomain.MatchString(s) && len(s) <= 253 }
func isValidEmail(s string) bool    { return reEmail.MatchString(s) && len(s) <= 254 }
func isValidSnap(s string) bool     { return reZfsDatasetSnap.MatchString(s) }
func isValidSnapName(s string) bool { return reSnapshotName.MatchString(s) }
func isValidDev(s string) bool      { return reDevName.MatchString(s) && len(s) <= 64 }
func isValidUnixUser(s string) bool { return reUnixUser.MatchString(s) && len(s) <= 32 }
func isValidContainer(s string) bool { return reContainerId.MatchString(s) && len(s) <= 128 }
func isValidWgIface(s string) bool  { return reWgInterface.MatchString(s) }
func isValidSafePath(s string) bool { return reAbsPath.MatchString(s) && !strings.Contains(s, "..") }

// ═══════════════════════════════════
// Share helpers
// ═══════════════════════════════════

func groupName(shareName string) string {
	return "nimos-share-" + shareName
}

// Share represents a share in shares.json
type Share struct {
	Name           string                 `json:"name"`
	Path           string                 `json:"path"`
	Permissions    map[string]string      `json:"permissions"`
	AppPermissions []map[string]interface{} `json:"appPermissions"`
}

// User represents a user in users.json
type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func readShares() ([]Share, error) {
	data, err := os.ReadFile(sharesFile)
	if err != nil {
		return nil, err
	}
	var shares []Share
	if err := json.Unmarshal(data, &shares); err != nil {
		return nil, err
	}
	return shares, nil
}

func readUsers() ([]User, error) {
	data, err := os.ReadFile(usersFile)
	if err != nil {
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func getSharePath(shareName string) (string, error) {
	if err := checkShareName(shareName); err != nil {
		return "", err
	}
	shares, err := readShares()
	if err != nil {
		return "", fmt.Errorf("cannot read shares config: %v", err)
	}
	for _, s := range shares {
		if s.Name == shareName {
			if s.Path == "" {
				return "", fmt.Errorf("share %q has no path", shareName)
			}
			if _, err := os.Stat(s.Path); os.IsNotExist(err) {
				return "", fmt.Errorf("share path does not exist: %s", s.Path)
			}
			return s.Path, nil
		}
	}
	return "", fmt.Errorf("share %q not found in config", shareName)
}

// ═══════════════════════════════════
// Request / Response types
// ═══════════════════════════════════

type Request struct {
	Op         string      `json:"op"`
	ShareName  string      `json:"shareName,omitempty"`
	PoolPath   string      `json:"poolPath,omitempty"`
	Username   string      `json:"username,omitempty"`
	Password   string      `json:"password,omitempty"`
	AppId      string      `json:"appId,omitempty"`
	Uid        interface{} `json:"uid,omitempty"`
	Permission string      `json:"permission,omitempty"`
}

type Response struct {
	Ok      bool        `json:"ok"`
	Error   string      `json:"error,omitempty"`
	Path    string      `json:"path,omitempty"`
	Existed bool        `json:"existed,omitempty"`
	Fixed   int         `json:"fixed,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ═══════════════════════════════════
// Operations catalog
// ═══════════════════════════════════

func handleOp(req Request) Response {
	switch req.Op {

	// ─── Share operations ───

	case "share.create":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		if err := checkPoolPath(req.PoolPath); err != nil {
			return Response{Error: err.Error()}
		}

		sharePath := filepath.Join(req.PoolPath, "shares", req.ShareName)
		group := groupName(req.ShareName)

		runSafe("groupadd", "-f", group)

		if err := os.MkdirAll(sharePath, 0770); err != nil {
			return Response{Error: fmt.Sprintf("cannot create directory: %v", err)}
		}

		runSafe("chown", "root:"+group, sharePath)
		runSafe("chmod", "2770", sharePath)
		runSafe("setfacl", "-d", "-m", "g:"+group+":rwx", sharePath)

		// Add service user
		if _, ok := runSafe("id", serviceUser); ok {
			runSafe("usermod", "-aG", group, serviceUser)
		}

		// Add admin users
		if users, err := readUsers(); err == nil {
			for _, u := range users {
				if u.Role == "admin" && validUsername.MatchString(u.Username) {
					runSafe("usermod", "-aG", group, u.Username)
				}
			}
		}

		logMsg("share.create: %s at %s (group: %s)", req.ShareName, sharePath, group)
		return Response{Ok: true, Path: sharePath}

	case "share.delete":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		group := groupName(req.ShareName)
		runSafe("groupdel", group)
		logMsg("share.delete: %s (group removed, files preserved)", req.ShareName)
		return Response{Ok: true}

	case "share.add_user_rw":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		if err := checkUsername(req.Username); err != nil {
			return Response{Error: err.Error()}
		}
		group := groupName(req.ShareName)
		if _, ok := runSafe("getent", "group", group); !ok {
			return Response{Error: fmt.Sprintf("group %s does not exist", group)}
		}
		runSafe("usermod", "-aG", group, req.Username)
		if sharePath, err := getSharePath(req.ShareName); err == nil {
			runSafe("setfacl", "-x", "u:"+req.Username, sharePath)
			runSafe("setfacl", "-d", "-x", "u:"+req.Username, sharePath)
		}
		logMsg("share.add_user_rw: %s → %s", req.Username, req.ShareName)
		return Response{Ok: true}

	case "share.add_user_ro":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		if err := checkUsername(req.Username); err != nil {
			return Response{Error: err.Error()}
		}
		sharePath, err := getSharePath(req.ShareName)
		if err != nil {
			return Response{Error: err.Error()}
		}
		group := groupName(req.ShareName)
		runSafe("gpasswd", "-d", req.Username, group)
		runSafe("setfacl", "-m", "u:"+req.Username+":r-x", sharePath)
		runSafe("setfacl", "-d", "-m", "u:"+req.Username+":r-x", sharePath)
		logMsg("share.add_user_ro: %s → %s", req.Username, req.ShareName)
		return Response{Ok: true}

	case "share.remove_user":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		if err := checkUsername(req.Username); err != nil {
			return Response{Error: err.Error()}
		}
		sharePath, err := getSharePath(req.ShareName)
		if err != nil {
			return Response{Error: err.Error()}
		}
		group := groupName(req.ShareName)
		runSafe("gpasswd", "-d", req.Username, group)
		runSafe("setfacl", "-x", "u:"+req.Username, sharePath)
		runSafe("setfacl", "-d", "-x", "u:"+req.Username, sharePath)
		logMsg("share.remove_user: %s ✕ %s", req.Username, req.ShareName)
		return Response{Ok: true}

	case "share.add_app":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		uid, err := checkUid(req.Uid)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if err := checkPermission(req.Permission); err != nil {
			return Response{Error: err.Error()}
		}
		sharePath, err := getSharePath(req.ShareName)
		if err != nil {
			return Response{Error: err.Error()}
		}
		acl := "r-x"
		if req.Permission == "rw" {
			acl = "rwx"
		}
		runSafe("setfacl", "-m", fmt.Sprintf("u:%d:%s", uid, acl), sharePath)
		runSafe("setfacl", "-d", "-m", fmt.Sprintf("u:%d:%s", uid, acl), sharePath)
		logMsg("share.add_app: %s (uid:%d) → %s (%s)", req.AppId, uid, req.ShareName, req.Permission)
		return Response{Ok: true}

	case "share.remove_app":
		if err := checkShareName(req.ShareName); err != nil {
			return Response{Error: err.Error()}
		}
		uid, err := checkUid(req.Uid)
		if err != nil {
			return Response{Error: err.Error()}
		}
		sharePath, err := getSharePath(req.ShareName)
		if err != nil {
			return Response{Error: err.Error()}
		}
		runSafe("setfacl", "-x", fmt.Sprintf("u:%d", uid), sharePath)
		runSafe("setfacl", "-d", "-x", fmt.Sprintf("u:%d", uid), sharePath)
		logMsg("share.remove_app: %s (uid:%d) ✕ %s", req.AppId, uid, req.ShareName)
		return Response{Ok: true}

	// ─── User operations ───

	case "user.create":
		if err := checkUsername(req.Username); err != nil {
			return Response{Error: err.Error()}
		}
		if _, ok := runSafe("id", req.Username); ok {
			logMsg("user.create: %s already exists — skipping", req.Username)
			return Response{Ok: true, Existed: true}
		}
		runSafe("useradd", "-M", "-s", "/usr/sbin/nologin", req.Username)
		if _, ok := runSafe("id", req.Username); !ok {
			return Response{Error: fmt.Sprintf("failed to create Linux user: %s", req.Username)}
		}
		logMsg("user.create: %s", req.Username)
		return Response{Ok: true}

	case "user.delete":
		if err := checkUsername(req.Username); err != nil {
			return Response{Error: err.Error()}
		}
		shell, _ := runSafe("sh", "-c", fmt.Sprintf(`getent passwd "%s" 2>/dev/null | cut -d: -f7`, req.Username))
		if !strings.Contains(shell, "nologin") {
			return Response{Error: fmt.Sprintf("refusing to delete %s: not a NimOS-managed user", req.Username)}
		}
		runSafe("smbpasswd", "-x", req.Username)
		runSafe("userdel", req.Username)
		logMsg("user.delete: %s", req.Username)
		return Response{Ok: true}

	case "user.set_smb_password":
		if err := checkUsername(req.Username); err != nil {
			return Response{Error: err.Error()}
		}
		if req.Password == "" {
			return Response{Error: "password required"}
		}
		// Ensure user exists
		if _, ok := runSafe("id", req.Username); !ok {
			runSafe("useradd", "-M", "-s", "/usr/sbin/nologin", req.Username)
		}
		// Set samba password via stdin
		cmd := exec.Command("smbpasswd", "-s", "-a", req.Username)
		cmd.Stdin = strings.NewReader(req.Password + "\n" + req.Password + "\n")
		if err := cmd.Run(); err != nil {
			return Response{Error: fmt.Sprintf("failed to set Samba password for %s", req.Username)}
		}
		logMsg("user.set_smb_password: %s", req.Username)
		return Response{Ok: true}

	// ─── System operations ───

	case "system.reconcile":
		return reconcile()

	// ─── NOTE: Database operations (db.*) removed from privileged daemon ───
	// HTTP handlers call db functions directly (dbUsersList, dbSharesGet, etc.)
	// The daemon socket only handles privileged OS operations (users, shares, ACLs)

	default:
		logMsg("rejected unknown op: %s", req.Op)
		return Response{Error: fmt.Sprintf("unknown operation: %s", req.Op)}
	}
}

// ═══════════════════════════════════
// Reconciliation
// ═══════════════════════════════════

func reconcile() Response {
	logMsg("system.reconcile: starting...")
	fixed := 0

	shares, err := dbSharesListRaw()
	if err != nil {
		logMsg("  reconcile error: %v", err)
		return Response{Error: err.Error(), Fixed: fixed}
	}

	for _, share := range shares {
		if share.Name == "" || share.Path == "" {
			continue
		}
		group := groupName(share.Name)

		// 1. Ensure group exists
		if _, ok := runSafe("getent", "group", group); !ok {
			runSafe("groupadd", "-f", group)
			logMsg("  reconcile: created group %s", group)
			fixed++
		}

		// 2. Ensure directory permissions (skip if quota is near full to avoid blocking)
		if _, err := os.Stat(share.Path); err == nil {
			avail := getAvailableBytes(share.Path)
			if avail < 1024*1024 { // less than 1MB free — skip permissions
				logMsg("  reconcile: skipping permissions for %s (disk full, %d bytes free)", share.Name, avail)
			} else {
				runSafe("chown", "root:"+group, share.Path)
				runSafe("chmod", "2770", share.Path)
				runSafe("setfacl", "-d", "-m", "g:"+group+":rwx", share.Path)
			}
		}

		// 3. Ensure user permissions match DB
		for username, perm := range share.Permissions {
			if !validUsername.MatchString(username) || systemUsers[username] {
				continue
			}
			if perm == "rw" {
				groups, ok := runSafe("id", "-nG", username)
				if ok && !containsWord(groups, group) {
					runSafe("usermod", "-aG", group, username)
					logMsg("  reconcile: added %s to %s (rw)", username, group)
					fixed++
				}
			} else if perm == "ro" {
				runSafe("gpasswd", "-d", username, group)
				runSafe("setfacl", "-m", "u:"+username+":r-x", share.Path)
				runSafe("setfacl", "-d", "-m", "u:"+username+":r-x", share.Path)
			}
		}

		// 4. Ensure app permissions
		for _, app := range share.AppPermissions {
			acl := "r-x"
			if app.Permission == "rw" {
				acl = "rwx"
			}
			runSafe("setfacl", "-m", fmt.Sprintf("u:%d:%s", app.Uid, acl), share.Path)
			runSafe("setfacl", "-d", "-m", fmt.Sprintf("u:%d:%s", app.Uid, acl), share.Path)
		}

		// 5. Service user must be in ALL share groups
		if _, ok := runSafe("id", serviceUser); ok {
			groups, _ := runSafe("id", "-nG", serviceUser)
			if !containsWord(groups, group) {
				runSafe("usermod", "-aG", group, serviceUser)
				logMsg("  reconcile: added service user %s to %s", serviceUser, group)
				fixed++
			}
		}

		// 6. Admin users always get rw on ALL shares
		if adminUsers, err := dbUsersListRaw(); err == nil {
			for _, u := range adminUsers {
				if u.Role == "admin" && validUsername.MatchString(u.Username) {
					groups, _ := runSafe("id", "-nG", u.Username)
					if !containsWord(groups, group) {
						runSafe("usermod", "-aG", group, u.Username)
						logMsg("  reconcile: added admin %s to %s", u.Username, group)
						fixed++
					}
				}
			}
		}
	}

	// Cleanup expired sessions
	cleaned := dbSessionCleanup()
	if cleaned > 0 {
		logMsg("  reconcile: cleaned %d expired sessions", cleaned)
	}

	logMsg("system.reconcile: done (%d fixes applied)", fixed)
	return Response{Ok: true, Fixed: fixed}
}

func containsWord(s, word string) bool {
	for _, w := range strings.Fields(s) {
		if w == word {
			return true
		}
	}
	return false
}

// ═══════════════════════════════════
// Socket server
// ═══════════════════════════════════

func main() {
	logMsg("NimOS Permissions Daemon starting...")
	logMsg("Socket: %s", socketPath)
	logMsg("Shares config: %s", sharesFile)
	logMsg("Database: %s", dbPath)

	// Initialize SQLite database
	if err := openDB(); err != nil {
		logMsg("Fatal: %v", err)
		os.Exit(1)
	}
	defer db.Close()
	logMsg("Database ready")

	// Migrate from JSON files (first run only)
	migrateFromJSON()

	// Initialize Beta 8 storage schema (idempotent)
	// see docs/storage_invariants.md#5
	if err := initStorageSchema(); err != nil {
		logMsg("ERROR: cannot initialize storage schema: %v", err)
		os.Exit(1)
	}
	logMsg("Storage schema (Beta 8) ready")

	// Initialize Beta 8 storage module (Repo + Policy singletons)
	if err := initStorageModule(); err != nil {
		logMsg("ERROR: cannot initialize storage module: %v", err)
		os.Exit(1)
	}

	// Beta 8 storage startup tasks: recovery de operations huérfanas
	// y boot reconciliation de devices. Best-effort; los fallos se
	// loggean pero no abortan el daemon.
	runStorageStartupTasks(context.Background())

	// Beta 8.1 v4 · Network module bootstrap.
	//
	// Orden estricto:
	//   1. nimos_core_schema  (secrets + breakers + capabilities globales)
	//   2. network_schema     (ports/ddns/certs/observed/operations/events)
	//   3. initNetworkModule  (singletons NetworkRepo + EventEmitter + Scheduler)
	//
	// Si cualquier paso falla, abortamos: el módulo network no puede
	// inicializarse parcialmente sin dejar la DB en estado inconsistente.
	if err := initNimosCoreSchema(db); err != nil {
		logMsg("ERROR: cannot initialize nimos core schema: %v", err)
		os.Exit(1)
	}
	if err := initNetworkSchema(db); err != nil {
		logMsg("ERROR: cannot initialize network schema: %v", err)
		os.Exit(1)
	}
	if err := initNetworkModule(); err != nil {
		logMsg("ERROR: cannot initialize network module: %v", err)
		os.Exit(1)
	}

	// Beta 8.1 · Apps bootstrap: escanea apps native ya instaladas en el
	// sistema (samba, kvm, transmission...) y las registra en native_apps
	// con auto_detected=1. Las apps desinstaladas manualmente se purgan.
	//
	// Async + best-effort: si alguna app tiene un CheckCommand lento, no
	// queremos bloquear el arranque del HTTP server. Tampoco abortamos si
	// el bootstrap falla — el resto del daemon sigue funcionando normal.
	go func() {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		bootstrapNativeApps(bootstrapCtx)
	}()

	// Start HTTP API server
	detectHardwareTools()
	startHTTPServer()
	startRateLimitCleanup()

	// Beta 8: arrancar el reconciler en background.
	// Desactivable con NIMOS_NO_STORAGE_SCHEDULER=1 para debugging
	// o despliegues controlados.
	if os.Getenv("NIMOS_NO_STORAGE_SCHEDULER") != "1" {
		StartStorageScheduler(context.Background())
	} else {
		logMsg("Storage scheduler disabled by NIMOS_NO_STORAGE_SCHEDULER=1")
	}

	// Fase 7 Bloque C1 · Storage Observer
	// Mantiene un cache in-memory del observed state (BTRFS detectados,
	// devices físicos, divergencias vs managed). Loop periódico 60s +
	// triggers desde ops internas (notifyStorageChanged).
	//
	// Desactivable con NIMOS_NO_STORAGE_OBSERVER=1 para debugging.
	// Diseño completo en docs/storage_observer_design.md.
	if os.Getenv("NIMOS_NO_STORAGE_OBSERVER") != "1" {
		globalObserver = NewStorageObserver(60 * time.Second)
		globalObserver.Start()
		logMsg("Storage observer started (interval=60s)")
	} else {
		logMsg("Storage observer disabled by NIMOS_NO_STORAGE_OBSERVER=1")
	}

	// FIRST: Mount all pools before anything else touches storage.
	// Beta 8: ZFS no longer supported; only BTRFS auto-mount.
	btrfsAutoMountOnStartup()
	startupStorage()

	// THEN: Start monitoring (cleanOrphanMountPoints runs here, AFTER pools are mounted)
	startStorageMonitoring()
	// Beta 8: ZFS scheduler removed. BTRFS scrub scheduling is handled by
	// startScrubScheduler() in storage_btrfs_features.go.

	// Start backup scheduler
	startBackupScheduler()
	startAutoDiscovery()
	//startWGTunnel()
	// Remount remote NFS shares in background — don't block daemon startup
	// If a remote host is unreachable, NFS mount can take minutes to timeout
	go remountAllOnStartup()

	// Reconcile service registry in background — don't block daemon startup
	// runCmd calls to systemctl/test can hang on NFS or slow services
	go func() {
		time.Sleep(3 * time.Second) // wait for socket to be ready
		reconcileServices()
	}()

	// Start scrub scheduler — checks every 60s if a scheduled verification is due
	go startScrubScheduler()

	// Start SMART monitor — checks disk health every 30 min, alerts on changes
	go startSmartMonitor()

	// Start config backup — saves NimOS config to each pool every 30 min
	// This enables pool restore after system disk failure + NimOS reinstall
	go startConfigBackupLoop()

	// Start NimShield security engine — honeypots, rules, blocklist
	go startShieldEngine()
	go startShieldCleanup()

	// Clean up stale socket
	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		logMsg("Fatal: cannot listen on %s: %v", socketPath, err)
		os.Exit(1)
	}
	defer listener.Close()

	// Set socket permissions: service user can connect
	os.Chmod(socketPath, 0660)
	// Change group to service user's group so Node.js server can connect
	runSafe("chgrp", serviceUser, socketPath)

	logMsg("Listening on %s", socketPath)

	// Run reconciliation on startup
	reconcile()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		logMsg("Shutting down (signal: %v)...", sig)
		stopBackupScheduler()
		stopAutoDiscovery()
		listener.Close()
		os.Remove(socketPath)
		os.Exit(0)
	}()

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if we're shutting down
			if strings.Contains(err.Error(), "use of closed") {
				break
			}
			logMsg("Accept error: %v", err)
			continue
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()

	// Read request with size limit
	data := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if len(data) > maxReqSize {
			writeResponse(conn, Response{Error: "request too large"})
			return
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			logMsg("Read error: %v", err)
			return
		}
	}

	// Parse request
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		writeResponse(conn, Response{Error: "invalid JSON"})
		return
	}

	if req.Op == "" {
		writeResponse(conn, Response{Error: "missing op"})
		return
	}

	// Log (mask password)
	logData := string(data)
	if req.Password != "" {
		logData = strings.Replace(logData, req.Password, "***", -1)
	}
	logMsg("→ %s %s", req.Op, logData)

	// Execute
	resp := handleOp(req)
	writeResponse(conn, resp)
}

func writeResponse(conn net.Conn, resp Response) {
	data, _ := json.Marshal(resp)
	conn.Write(data)
}
