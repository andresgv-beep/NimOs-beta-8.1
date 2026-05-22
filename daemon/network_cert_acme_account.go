// network_cert_acme_account.go — Gestión de la account key ACME.
//
// ACME (RFC 8555) requiere que cada cliente tenga una "account key" —
// un par criptográfico que identifica al cliente ante el CA. La pubkey
// se registra en el CA junto con un email opcional, y la priv firma
// cada request subsiguiente.
//
// Decisiones tomadas con el usuario:
//
//   - **ECDSA P-256**: estándar moderno en ACME, más rápido que RSA en
//     Raspberry Pi, footprint menor en disco. RSA sigue siendo válido
//     pero algunos endpoints empiezan a deprecarlo para accounts nuevas.
//
//   - **Storage**: archivo plano en /var/lib/nimos/acme/account.key con
//     chmod 600. La account key NO da acceso a nada útil para un
//     atacante (solo le permite renovar certs de dominios que él
//     verifique poseer). Cifrarla en nimos_secrets añadiría complejidad
//     sin ganancia real. Mantenemos simplicidad para debug.
//
//   - **Una sola account key compartida** entre todos los certs del
//     daemon. ACME permite registrar múltiples accounts pero no hay
//     razón en NimOS — un solo binario, una sola identidad.
//
// Esta función NO crea el cliente ACME ni hace registration con el CA.
// Eso lo hace LetsEncryptProvider cuando se le pasa la key.

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultACMEAccountKeyPath es la ubicación canónica de la account key
// ACME. Tests usan rutas temporales.
const DefaultACMEAccountKeyPath = "/var/lib/nimos/acme/account.key"

// LoadOrCreateACMEAccountKey lee la account key desde disco o genera
// una nueva si no existe. Devuelve la clave ECDSA lista para usar con
// el cliente ACME.
//
// Si el archivo existe pero está corrupto, devuelve error en lugar de
// sobrescribirlo — la decisión de borrar y regenerar la toma el caller
// para no perder accidentalmente una cuenta registrada.
//
// El archivo se crea con permisos 0600 y el directorio con 0700.
func LoadOrCreateACMEAccountKey(path string) (*ecdsa.PrivateKey, error) {
	// Intentar leer existente.
	data, err := os.ReadFile(path)
	if err == nil {
		return parseACMEAccountKeyPEM(data)
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("acme account key: read %s: %w", path, err)
	}

	// No existe → generar nueva.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme account key: generate: %w", err)
	}

	// Asegurar directorio.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("acme account key: mkdir %s: %w", dir, err)
	}

	// Codificar y escribir atómicamente (write tmp + rename).
	pemBytes, err := encodeACMEAccountKeyPEM(key)
	if err != nil {
		return nil, fmt.Errorf("acme account key: encode: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("acme account key: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("acme account key: rename: %w", err)
	}
	// Defensivo: re-chmod por si umask interfiere.
	_ = os.Chmod(path, 0o600)

	return key, nil
}

// parseACMEAccountKeyPEM lee bytes PEM y devuelve la clave ECDSA.
// Soporta tanto "EC PRIVATE KEY" (PKCS#1-style) como "PRIVATE KEY"
// (PKCS#8) para tolerar archivos generados por otras herramientas.
func parseACMEAccountKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("acme account key: no PEM block")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("acme account key: parse EC: %w", err)
		}
		if key.Curve != elliptic.P256() {
			return nil, fmt.Errorf("acme account key: expected P-256, got %s", key.Curve.Params().Name)
		}
		return key, nil
	case "PRIVATE KEY":
		anyKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("acme account key: parse PKCS8: %w", err)
		}
		key, ok := anyKey.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("acme account key: expected ECDSA, got %T", anyKey)
		}
		if key.Curve != elliptic.P256() {
			return nil, fmt.Errorf("acme account key: expected P-256, got %s", key.Curve.Params().Name)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("acme account key: unexpected PEM type %q", block.Type)
	}
}

// encodeACMEAccountKeyPEM codifica una clave ECDSA en PEM tipo
// "EC PRIVATE KEY". Formato compacto y compatible con herramientas
// como openssl.
func encodeACMEAccountKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	}), nil
}
