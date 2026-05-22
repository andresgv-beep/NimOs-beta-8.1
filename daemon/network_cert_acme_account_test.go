// network_cert_acme_account_test.go — Tests de account key ACME.
//
// Cubre:
//   - Generación inicial cuando no existe el archivo.
//   - Carga de archivo existente (round-trip).
//   - Permisos correctos del archivo y directorio.
//   - Detección de PEM corrupto.
//   - Rechazo de curvas distintas a P-256.
//   - Soporte de ambos formatos: EC PRIVATE KEY y PRIVATE KEY (PKCS8).

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// LoadOrCreateACMEAccountKey
// ─────────────────────────────────────────────────────────────────────────────

func TestACMEAccountKey_CreatesWhenMissing(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "acme", "account.key")

	key, err := LoadOrCreateACMEAccountKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if key == nil {
		t.Fatal("key is nil")
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("curve = %s, want P-256", key.Curve.Params().Name)
	}

	// Archivo debe existir tras la creación.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s: %v", path, err)
	}
}

func TestACMEAccountKey_LoadExistingRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "account.key")

	// Primera llamada genera.
	k1, err := LoadOrCreateACMEAccountKey(path)
	if err != nil {
		t.Fatal(err)
	}

	// Segunda llamada lee el existente — debe ser la misma key.
	k2, err := LoadOrCreateACMEAccountKey(path)
	if err != nil {
		t.Fatal(err)
	}

	// Comparar D (escalar privado) — si coinciden es la misma key.
	if k1.D.Cmp(k2.D) != 0 {
		t.Error("second load produced different key (regenerated instead of read)")
	}
}

func TestACMEAccountKey_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes not meaningful on Windows")
	}
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subdir", "account.key")

	_, err := LoadOrCreateACMEAccountKey(path)
	if err != nil {
		t.Fatal(err)
	}

	// Archivo: 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}

	// Directorio: 0700.
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
}

func TestACMEAccountKey_CorruptedFileReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "account.key")
	if err := os.WriteFile(path, []byte("not a PEM file at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadOrCreateACMEAccountKey(path)
	if err == nil {
		t.Error("expected error for corrupted file")
	}
}

func TestACMEAccountKey_RejectsWrongCurve(t *testing.T) {
	// Generar una key P-384 y guardarla; cargarla debe fallar.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "account.key")

	wrongCurveKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalECPrivateKey(wrongCurveKey)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadOrCreateACMEAccountKey(path)
	if err == nil {
		t.Error("expected error for P-384 key (only P-256 allowed)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parse / encode round-trips
// ─────────────────────────────────────────────────────────────────────────────

func TestACMEAccountKey_PKCS8FormatSupported(t *testing.T) {
	// Algunas herramientas emiten PKCS#8 ("PRIVATE KEY") en lugar de
	// "EC PRIVATE KEY". Soportar ambos.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	parsed, err := parseACMEAccountKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.D.Cmp(key.D) != 0 {
		t.Error("parsed key differs from original")
	}
}

func TestACMEAccountKey_UnknownPEMTypeRejected(t *testing.T) {
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: []byte("doesn't matter"),
	})
	_, err := parseACMEAccountKeyPEM(pemBytes)
	if err == nil {
		t.Error("expected error for unknown PEM type")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Atomic write
// ─────────────────────────────────────────────────────────────────────────────

func TestACMEAccountKey_NoTempFileLeftAfterSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "account.key")

	if _, err := LoadOrCreateACMEAccountKey(path); err != nil {
		t.Fatal(err)
	}

	// No debe quedar archivo .tmp.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file not cleaned up: %v", err)
	}
}
