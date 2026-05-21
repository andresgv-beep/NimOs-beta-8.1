// network_repo_test.go — Tests del NetworkRepo (Ports/Ddns/Certs).
//
// Estrategia:
//   - DB temporal con core + network schema aplicados (setupNetworkDB).
//   - FakeClock para timestamps deterministas.
//   - Helpers para sembrar entidades comunes.
//   - Cada test usa su propia DB y cleanup defer.
//
// Cobertura objetivo:
//   - CRUD básico de cada entidad.
//   - Triple generation: estado inicial, UpdateConfig incrementa desired,
//     RecordObserved incrementa observed, MarkApplied sincroniza applied.
//   - Convergence helpers (IsConverged, HasDrifted, IsPending).
//   - Queries List*Pending / List*Drifted devuelven correctamente.
//   - FK constraints reales (DDNS sin secret válido → error claro).
//   - NotFound errors.
//   - Errors de UNIQUE.

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newTestRepo monta DB con schemas aplicados + repo + clock.
func newTestRepo(t *testing.T) (*NetworkRepo, *FakeClock, *sqlConn, func()) {
	t.Helper()
	c, cleanup := setupNetworkDB(t)
	clock := NewFakeClock(time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC))
	repo := NewNetworkRepo(c.db, clock)
	return repo, clock, c, cleanup
}

// withNetTx ejecuta fn dentro de una transacción y commit. Test helper.
func withNetTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("tx fn: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// seedSecret inserta un secret mínimo y devuelve su ID.
func seedSecret(t *testing.T, db *sql.DB, label string) string {
	t.Helper()
	// Usar el SecretsStore real para que el formato sea válido.
	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	store, err := NewSecretsStoreWithKey(db, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateSecret("ddns_token", label, []byte("test-token-"+label))
	if err != nil {
		t.Fatal(err)
	}
	return string(id)
}

// ═════════════════════════════════════════════════════════════════════════════
// Convergence helpers
// ═════════════════════════════════════════════════════════════════════════════

func TestConvergence_Helpers(t *testing.T) {
	cases := []struct {
		name      string
		c         Convergence
		converged bool
		drifted   bool
		pending   bool
	}{
		{"initial after create", Convergence{1, 0, 0}, false, false, true},
		{"converged", Convergence{2, 2, 2}, true, false, false},
		{"pending only", Convergence{3, 2, 2}, false, false, true},
		{"drifted only", Convergence{2, 5, 2}, true, true, false},
		{"both pending and drifted", Convergence{5, 4, 2}, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.IsConverged(); got != tc.converged {
				t.Errorf("IsConverged() = %v, want %v", got, tc.converged)
			}
			if got := tc.c.HasDrifted(); got != tc.drifted {
				t.Errorf("HasDrifted() = %v, want %v", got, tc.drifted)
			}
			if got := tc.c.IsPending(); got != tc.pending {
				t.Errorf("IsPending() = %v, want %v", got, tc.pending)
			}
		})
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Ports
// ═════════════════════════════════════════════════════════════════════════════

func TestPorts_CreateAndGet_RoundTrip(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{
		ID:          "http",
		Port:        8080,
		BindAddress: "0.0.0.0",
		Enabled:     true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreatePort(context.Background(), tx, p)
	})

	// Estado inicial de convergence: desired=1, observed=0, applied=0
	if p.Convergence != (Convergence{Desired: 1, Observed: 0, Applied: 0}) {
		t.Errorf("convergence post-create = %+v, want {1,0,0}", p.Convergence)
	}
	if !p.UpdatedAt.Equal(clock.Now().UTC()) {
		t.Errorf("UpdatedAt = %v, want %v", p.UpdatedAt, clock.Now().UTC())
	}

	got, err := repo.GetPort(context.Background(), "http")
	if err != nil {
		t.Fatalf("GetPort: %v", err)
	}
	if got.Port != 8080 || got.BindAddress != "0.0.0.0" || !got.Enabled {
		t.Errorf("got %+v, want fields preserved", got)
	}
	if got.Convergence.Desired != 1 || got.Convergence.Applied != 0 {
		t.Errorf("convergence = %+v, want {1,0,0}", got.Convergence)
	}
}

func TestPorts_RejectsInvalidID(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{ID: "ftp", Port: 21, BindAddress: "0.0.0.0", Enabled: true}
	tx, _ := c.db.Begin()
	defer tx.Rollback()

	err := repo.CreatePort(context.Background(), tx, p)
	if !errors.Is(err, ErrInvalidPortID) {
		t.Errorf("err = %v, want ErrInvalidPortID", err)
	}
}

func TestPorts_GetNotFound(t *testing.T) {
	repo, _, _, cleanup := newTestRepo(t)
	defer cleanup()

	_, err := repo.GetPort(context.Background(), "http")
	if !errors.Is(err, ErrPortNotFound) {
		t.Errorf("err = %v, want ErrPortNotFound", err)
	}
}

func TestPorts_DuplicateCreate(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{ID: "http", Port: 8080, BindAddress: "0.0.0.0", Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreatePort(context.Background(), tx, p) })

	tx, _ := c.db.Begin()
	defer tx.Rollback()
	err := repo.CreatePort(context.Background(), tx, p)
	if !errors.Is(err, ErrPortAlreadyExists) {
		t.Errorf("err = %v, want ErrPortAlreadyExists", err)
	}
}

func TestPorts_ListReturnsAll(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.CreatePort(context.Background(), tx,
			&NetworkPort{ID: "http", Port: 80, BindAddress: "0.0.0.0", Enabled: true}); err != nil {
			return err
		}
		return repo.CreatePort(context.Background(), tx,
			&NetworkPort{ID: "https", Port: 443, BindAddress: "0.0.0.0", Enabled: true})
	})

	got, err := repo.ListPorts(context.Background())
	if err != nil {
		t.Fatalf("ListPorts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d ports, want 2", len(got))
	}
	if got[0].ID != "http" || got[1].ID != "https" {
		t.Errorf("order = %s,%s; want http,https", got[0].ID, got[1].ID)
	}
}

func TestPorts_UpdateConfigIncrementsDesired(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{ID: "http", Port: 80, BindAddress: "0.0.0.0", Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreatePort(context.Background(), tx, p) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.UpdatePortConfig(context.Background(), tx, "http", 8080, "127.0.0.1", false)
	})

	got, _ := repo.GetPort(context.Background(), "http")
	if got.Port != 8080 || got.BindAddress != "127.0.0.1" || got.Enabled {
		t.Errorf("update did not persist fields: %+v", got)
	}
	if got.Convergence.Desired != 2 {
		t.Errorf("desired_generation = %d, want 2 (initial 1 + update)", got.Convergence.Desired)
	}
	if got.Convergence.Applied != 0 {
		t.Errorf("applied_generation = %d, want 0 (not reconciled yet)", got.Convergence.Applied)
	}
}

func TestPorts_UpdateNonExistent(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	tx, _ := c.db.Begin()
	defer tx.Rollback()
	err := repo.UpdatePortConfig(context.Background(), tx, "http", 8080, "0.0.0.0", true)
	if !errors.Is(err, ErrPortNotFound) {
		t.Errorf("err = %v, want ErrPortNotFound", err)
	}
}

func TestPorts_RecordObservedIncrementsObserved(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{ID: "http", Port: 80, BindAddress: "0.0.0.0", Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreatePort(context.Background(), tx, p) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.RecordPortObserved(context.Background(), tx, "http")
	})

	got, _ := repo.GetPort(context.Background(), "http")
	if got.Convergence.Observed != 1 {
		t.Errorf("observed_generation = %d, want 1", got.Convergence.Observed)
	}
	if !got.Convergence.HasDrifted() {
		t.Error("HasDrifted() should be true (observed=1, applied=0)")
	}
}

func TestPorts_MarkAppliedSyncsWithDesired(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{ID: "http", Port: 80, BindAddress: "0.0.0.0", Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreatePort(context.Background(), tx, p) })

	// Update config 3 veces → desired llega a 4
	for i := 0; i < 3; i++ {
		withNetTx(t, c.db, func(tx *sql.Tx) error {
			return repo.UpdatePortConfig(context.Background(), tx, "http", 80+i, "0.0.0.0", true)
		})
	}

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.MarkPortApplied(context.Background(), tx, "http")
	})

	got, _ := repo.GetPort(context.Background(), "http")
	if got.Convergence.Applied != 4 {
		t.Errorf("applied = %d, want 4", got.Convergence.Applied)
	}
	if !got.Convergence.IsConverged() {
		t.Error("IsConverged() should be true after MarkApplied")
	}
}

func TestPorts_ListPendingAndDrifted(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	// Crear http y https
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.CreatePort(context.Background(), tx,
			&NetworkPort{ID: "http", Port: 80, BindAddress: "0.0.0.0", Enabled: true}); err != nil {
			return err
		}
		return repo.CreatePort(context.Background(), tx,
			&NetworkPort{ID: "https", Port: 443, BindAddress: "0.0.0.0", Enabled: true})
	})

	// Aplicar https
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.MarkPortApplied(context.Background(), tx, "https")
	})

	// Drift en https
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.RecordPortObserved(context.Background(), tx, "https")
	})

	// Estado:
	//   http:  desired=1, observed=0, applied=0 → PENDING (1>0), NOT drifted (0==0)
	//   https: desired=1, observed=1, applied=1 → CONVERGED + DRIFTED (1!=1 → no, igual)

	// Wait — observed=1 applied=1 → no drift. Necesito que observed != applied.
	// Re-arquitectar el test: aplicar https, recordObserved DESPUÉS, así observed=1, applied=1 → no drift.

	// Mejor escenario:
	// - http:  PENDING (desired=1 > applied=0)
	// - https: aplicado entonces drifted (observed=1, applied=1 inicial → no drift).
	//          Para drift: incrementar observed sin tocar applied → applied=1 observed=2 → DRIFT.

	// Forzar drift en https:
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.RecordPortObserved(context.Background(), tx, "https")
	})
	// Ahora https: applied=1, observed=2 → DRIFTED

	pending, err := repo.ListPendingPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "http" {
		t.Errorf("pending = %v, want only [http]", portIDs(pending))
	}

	drifted, err := repo.ListDriftedPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// http: observed=0 applied=0 → no drift
	// https: observed=2 applied=1 → DRIFT
	if len(drifted) != 1 || drifted[0].ID != "https" {
		t.Errorf("drifted = %v, want only [https]", portIDs(drifted))
	}
}

func portIDs(ps []*NetworkPort) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

func TestPorts_Delete(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	p := &NetworkPort{ID: "http", Port: 80, BindAddress: "0.0.0.0", Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreatePort(context.Background(), tx, p) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.DeletePort(context.Background(), tx, "http")
	})

	_, err := repo.GetPort(context.Background(), "http")
	if !errors.Is(err, ErrPortNotFound) {
		t.Errorf("after delete: err = %v, want ErrPortNotFound", err)
	}
}

func TestPorts_DeleteIdempotent(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.DeletePort(context.Background(), tx, "nonexistent")
	})
	// no panic, no error
}

// ═════════════════════════════════════════════════════════════════════════════
// DDNS
// ═════════════════════════════════════════════════════════════════════════════

func TestDdns_CreateAndGet_RoundTrip(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	secretID := seedSecret(t, c.db, "nimosbarraca")

	d := &NetworkDdns{
		Provider:      "duckdns",
		Domain:        "nimosbarraca.duckdns.org",
		TokenSecretID: secretID,
		Enabled:       true,
		AutoUpdate:    true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateDdns(context.Background(), tx, d)
	})

	if d.ID == "" {
		t.Error("CreateDdns did not generate UUID")
	}
	if d.UpdateInterval != 900 {
		t.Errorf("default UpdateInterval = %d, want 900", d.UpdateInterval)
	}
	if d.Convergence != (Convergence{Desired: 1}) {
		t.Errorf("initial convergence = %+v, want {1,0,0}", d.Convergence)
	}

	got, err := repo.GetDdns(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("GetDdns: %v", err)
	}
	if got.Domain != d.Domain || got.Provider != d.Provider {
		t.Errorf("got %+v, want fields preserved", got)
	}
	if got.LastRunAt != nil || got.LastRunResult != nil || got.LastIP != nil {
		t.Error("initial nullables should be nil")
	}
}

func TestDdns_RejectsMissingSecretFK(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	d := &NetworkDdns{
		Provider:      "duckdns",
		Domain:        "x.duckdns.org",
		TokenSecretID: "does-not-exist-in-secrets",
		Enabled:       true,
	}
	tx, _ := c.db.Begin()
	defer tx.Rollback()

	err := repo.CreateDdns(context.Background(), tx, d)
	if err == nil {
		t.Fatal("expected FK error, got nil")
	}
	// El mensaje debe ser útil (contiene "token_secret_id" o "does-not-exist")
	msg := err.Error()
	if !strings.Contains(msg, "token_secret_id") && !strings.Contains(msg, "does-not-exist") {
		t.Errorf("error message not helpful: %v", err)
	}
}

func TestDdns_DuplicateDomain(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	s1 := seedSecret(t, c.db, "a")
	s2 := seedSecret(t, c.db, "b")

	d1 := &NetworkDdns{Provider: "duckdns", Domain: "same.duckdns.org", TokenSecretID: s1, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d1) })

	d2 := &NetworkDdns{Provider: "noip", Domain: "same.duckdns.org", TokenSecretID: s2, Enabled: true}
	tx, _ := c.db.Begin()
	defer tx.Rollback()
	err := repo.CreateDdns(context.Background(), tx, d2)
	if err == nil {
		t.Error("UNIQUE(domain) should reject duplicate")
	}
}

func TestDdns_GetByDomain(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	got, err := repo.GetDdnsByDomain(context.Background(), "x.duckdns.org")
	if err != nil {
		t.Fatalf("GetDdnsByDomain: %v", err)
	}
	if got.ID != d.ID {
		t.Errorf("ID mismatch: got %s want %s", got.ID, d.ID)
	}

	_, err = repo.GetDdnsByDomain(context.Background(), "nope.duckdns.org")
	if !errors.Is(err, ErrDdnsNotFound) {
		t.Errorf("err = %v, want ErrDdnsNotFound", err)
	}
}

func TestDdns_UpdateConfigIncrementsDesired(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()

	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true, AutoUpdate: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.UpdateDdnsConfig(context.Background(), tx, d.ID, false, false, 600)
	})

	got, _ := repo.GetDdns(context.Background(), d.ID)
	if got.Enabled || got.AutoUpdate || got.UpdateInterval != 600 {
		t.Errorf("update did not persist: %+v", got)
	}
	if got.Convergence.Desired != 2 {
		t.Errorf("desired = %d, want 2", got.Convergence.Desired)
	}
}

func TestDdns_RecordRunPersistsResult(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	ip := "1.2.3.4"
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.RecordDdnsRun(context.Background(), tx, d.ID, "success", &ip)
	})

	got, _ := repo.GetDdns(context.Background(), d.ID)
	if got.LastRunResult == nil || *got.LastRunResult != "success" {
		t.Errorf("LastRunResult = %v, want success", got.LastRunResult)
	}
	if got.LastIP == nil || *got.LastIP != "1.2.3.4" {
		t.Errorf("LastIP = %v, want 1.2.3.4", got.LastIP)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(clock.Now().UTC()) {
		t.Errorf("LastRunAt = %v, want %v", got.LastRunAt, clock.Now().UTC())
	}
}

func TestDdns_RecordRunRejectsInvalidResult(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	tx, _ := c.db.Begin()
	defer tx.Rollback()
	err := repo.RecordDdnsRun(context.Background(), tx, d.ID, "weird", nil)
	if err == nil {
		t.Error("should reject result='weird'")
	}
}

func TestDdns_MarkApplied(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.UpdateDdnsConfig(context.Background(), tx, d.ID, true, true, 600)
	})
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.MarkDdnsApplied(context.Background(), tx, d.ID)
	})

	got, _ := repo.GetDdns(context.Background(), d.ID)
	if got.Convergence.Applied != 2 {
		t.Errorf("applied = %d, want 2", got.Convergence.Applied)
	}
	if !got.Convergence.IsConverged() {
		t.Error("should be converged")
	}
}

func TestDdns_ListReturnsAllSortedByDomain(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	s1 := seedSecret(t, c.db, "a")
	s2 := seedSecret(t, c.db, "b")
	s3 := seedSecret(t, c.db, "c")

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.CreateDdns(context.Background(), tx,
			&NetworkDdns{Provider: "duckdns", Domain: "zeta.duckdns.org", TokenSecretID: s1, Enabled: true}); err != nil {
			return err
		}
		if err := repo.CreateDdns(context.Background(), tx,
			&NetworkDdns{Provider: "duckdns", Domain: "alpha.duckdns.org", TokenSecretID: s2, Enabled: false}); err != nil {
			return err
		}
		return repo.CreateDdns(context.Background(), tx,
			&NetworkDdns{Provider: "noip", Domain: "mike.example.org", TokenSecretID: s3, Enabled: true})
	})

	got, err := repo.ListDdns(context.Background())
	if err != nil {
		t.Fatalf("ListDdns: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	wantOrder := []string{"alpha.duckdns.org", "mike.example.org", "zeta.duckdns.org"}
	for i, d := range got {
		if d.Domain != wantOrder[i] {
			t.Errorf("[%d] = %s, want %s", i, d.Domain, wantOrder[i])
		}
	}
}

func TestDdns_ListPendingFiltersDisabled(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	s1 := seedSecret(t, c.db, "a")
	s2 := seedSecret(t, c.db, "b")

	// d1 enabled, pending
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateDdns(context.Background(), tx,
			&NetworkDdns{Provider: "duckdns", Domain: "a.duckdns.org", TokenSecretID: s1, Enabled: true})
	})
	// d2 disabled, pending — debe excluirse
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateDdns(context.Background(), tx,
			&NetworkDdns{Provider: "duckdns", Domain: "b.duckdns.org", TokenSecretID: s2, Enabled: false})
	})

	pending, _ := repo.ListPendingDdns(context.Background())
	if len(pending) != 1 || pending[0].Domain != "a.duckdns.org" {
		t.Errorf("pending = %v, want only [a.duckdns.org]", ddnsDomains(pending))
	}
}

func ddnsDomains(ds []*NetworkDdns) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Domain
	}
	return out
}

func TestDdns_DeletePreservesSecret(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.DeleteDdns(context.Background(), tx, d.ID)
	})

	// El secret debe seguir existiendo.
	var count int
	c.db.QueryRow(`SELECT COUNT(*) FROM nimos_secrets WHERE id = ?`, secretID).Scan(&count)
	if count != 1 {
		t.Errorf("DeleteDdns should NOT cascade to secret; got count=%d", count)
	}
}

func TestDdns_CascadeOnSecretDelete(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	secretID := seedSecret(t, c.db, "a")
	d := &NetworkDdns{Provider: "duckdns", Domain: "x.duckdns.org", TokenSecretID: secretID, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateDdns(context.Background(), tx, d) })

	// Borrar el secret debe cascadear al ddns.
	if _, err := c.db.Exec(`DELETE FROM nimos_secrets WHERE id = ?`, secretID); err != nil {
		t.Fatal(err)
	}
	_, err := repo.GetDdns(context.Background(), d.ID)
	if !errors.Is(err, ErrDdnsNotFound) {
		t.Errorf("ddns should be gone after secret delete; err = %v", err)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Certs
// ═════════════════════════════════════════════════════════════════════════════

func TestCerts_SelfSignedRoundTrip(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	notAfter := clock.Now().Add(365 * 24 * time.Hour).UTC()
	cert := &NetworkCert{
		Domain:        "host.local",
		CertProvider:  "selfsigned",
		FullchainPath: "/etc/ssl/fullchain.pem",
		PrivkeyPath:   "/etc/ssl/privkey.pem",
		NotBefore:     clock.Now().UTC(),
		NotAfter:      notAfter,
		Enabled:       true,
		AutoRenew:     true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateCert(context.Background(), tx, cert)
	})

	if cert.ID == "" {
		t.Error("ID not generated")
	}
	if cert.IssuedAt.IsZero() {
		t.Error("IssuedAt not set by default")
	}

	got, err := repo.GetCert(context.Background(), cert.ID)
	if err != nil {
		t.Fatalf("GetCert: %v", err)
	}
	if got.ChallengeType != nil || got.DNSProvider != nil {
		t.Error("self-signed cert should have nil challenge_type and dns_provider")
	}
	if !got.NotAfter.Equal(notAfter) {
		t.Errorf("NotAfter = %v, want %v", got.NotAfter, notAfter)
	}
}

func TestCerts_DNS01WithProvider(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	challenge := "dns-01"
	dnsProv := "duckdns"
	cert := &NetworkCert{
		Domain:        "host.duckdns.org",
		CertProvider:  "letsencrypt",
		ChallengeType: &challenge,
		DNSProvider:   &dnsProv,
		FullchainPath: "/etc/ssl/fullchain.pem",
		PrivkeyPath:   "/etc/ssl/privkey.pem",
		NotBefore:     clock.Now().UTC(),
		NotAfter:      clock.Now().Add(90 * 24 * time.Hour).UTC(),
		Enabled:       true,
		AutoRenew:     true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.CreateCert(context.Background(), tx, cert)
	})

	got, _ := repo.GetCert(context.Background(), cert.ID)
	if got.ChallengeType == nil || *got.ChallengeType != "dns-01" {
		t.Errorf("ChallengeType = %v, want dns-01", got.ChallengeType)
	}
	if got.DNSProvider == nil || *got.DNSProvider != "duckdns" {
		t.Errorf("DNSProvider = %v, want duckdns", got.DNSProvider)
	}
}

func TestCerts_IncoherentChallengeRejected(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	// http-01 con dns_provider → CHECK falla
	challenge := "http-01"
	dnsProv := "duckdns"
	cert := &NetworkCert{
		Domain:        "x.com",
		CertProvider:  "letsencrypt",
		ChallengeType: &challenge,
		DNSProvider:   &dnsProv, // incoherente con http-01
		FullchainPath: "/a",
		PrivkeyPath:   "/b",
		NotBefore:     clock.Now().UTC(),
		NotAfter:      clock.Now().Add(time.Hour).UTC(),
	}
	tx, _ := c.db.Begin()
	defer tx.Rollback()

	err := repo.CreateCert(context.Background(), tx, cert)
	if err == nil {
		t.Error("CHECK should reject http-01 + non-null dns_provider")
	}
}

func TestCerts_GetByDomain(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	cert := &NetworkCert{
		Domain: "host.local", CertProvider: "selfsigned",
		FullchainPath: "/a", PrivkeyPath: "/b",
		NotBefore: clock.Now().UTC(), NotAfter: clock.Now().Add(time.Hour).UTC(),
		Enabled: true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateCert(context.Background(), tx, cert) })

	got, err := repo.GetCertByDomain(context.Background(), "host.local")
	if err != nil {
		t.Fatalf("GetCertByDomain: %v", err)
	}
	if got.ID != cert.ID {
		t.Errorf("ID mismatch")
	}
}

func TestCerts_DuplicateDomainRejected(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	makeCert := func() *NetworkCert {
		return &NetworkCert{
			Domain: "same.local", CertProvider: "selfsigned",
			FullchainPath: "/a", PrivkeyPath: "/b",
			NotBefore: clock.Now().UTC(), NotAfter: clock.Now().Add(time.Hour).UTC(),
			Enabled: true,
		}
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateCert(context.Background(), tx, makeCert()) })

	tx, _ := c.db.Begin()
	defer tx.Rollback()
	err := repo.CreateCert(context.Background(), tx, makeCert())
	if err == nil {
		t.Error("UNIQUE(domain) should reject duplicate")
	}
}

func TestCerts_ListExpiringWithinWindow(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	mkCert := func(domain string, notAfter time.Time, autoRenew bool) *NetworkCert {
		return &NetworkCert{
			Domain: domain, CertProvider: "selfsigned",
			FullchainPath: "/a", PrivkeyPath: "/b",
			NotBefore: clock.Now().UTC(), NotAfter: notAfter,
			Enabled: true, AutoRenew: autoRenew,
		}
	}

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		// Cert que expira en 5d (debe aparecer si window=30d)
		if err := repo.CreateCert(context.Background(), tx,
			mkCert("soon.local", clock.Now().Add(5*24*time.Hour).UTC(), true)); err != nil {
			return err
		}
		// Cert que expira en 60d (NO debe aparecer)
		if err := repo.CreateCert(context.Background(), tx,
			mkCert("later.local", clock.Now().Add(60*24*time.Hour).UTC(), true)); err != nil {
			return err
		}
		// Cert que expira en 5d pero auto_renew=0 (NO debe aparecer)
		return repo.CreateCert(context.Background(), tx,
			mkCert("no-renew.local", clock.Now().Add(5*24*time.Hour).UTC(), false))
	})

	got, err := repo.ListExpiringCerts(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("ListExpiringCerts: %v", err)
	}
	if len(got) != 1 || got[0].Domain != "soon.local" {
		t.Errorf("got %v, want only [soon.local]", certDomains(got))
	}
}

func certDomains(cs []*NetworkCert) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Domain
	}
	return out
}

func TestCerts_UpdateRenewedPersistsFields(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	cert := &NetworkCert{
		Domain: "host.local", CertProvider: "selfsigned",
		FullchainPath: "/old/fullchain", PrivkeyPath: "/old/privkey",
		NotBefore: clock.Now().UTC(), NotAfter: clock.Now().Add(time.Hour).UTC(),
		Enabled: true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateCert(context.Background(), tx, cert) })

	newNotAfter := clock.Now().Add(90 * 24 * time.Hour).UTC()
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.UpdateCertRenewed(context.Background(), tx, cert.ID,
			"/new/fullchain", "/new/privkey",
			clock.Now().UTC(), newNotAfter)
	})

	got, _ := repo.GetCert(context.Background(), cert.ID)
	if got.FullchainPath != "/new/fullchain" {
		t.Errorf("FullchainPath = %q, want /new/fullchain", got.FullchainPath)
	}
	if !got.NotAfter.Equal(newNotAfter) {
		t.Errorf("NotAfter = %v, want %v", got.NotAfter, newNotAfter)
	}
	if got.LastRenewedAt == nil || !got.LastRenewedAt.Equal(clock.Now().UTC()) {
		t.Errorf("LastRenewedAt = %v, want %v", got.LastRenewedAt, clock.Now().UTC())
	}
}

func TestCerts_TripleGenerationFlow(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	cert := &NetworkCert{
		Domain: "host.local", CertProvider: "selfsigned",
		FullchainPath: "/a", PrivkeyPath: "/b",
		NotBefore: clock.Now().UTC(), NotAfter: clock.Now().Add(time.Hour).UTC(),
		Enabled: true, AutoRenew: true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateCert(context.Background(), tx, cert) })

	// Flujo realista bajo el modelo correcto:
	//
	//   1. Create        → d=1, o=0, a=0  (pending: a<d)
	//   2. SetAutoRenew  → d=2, o=0, a=0  (pending: a<d)
	//   3. MarkApplied   → d=2, o=2, a=2  (converged, no drift)
	//   4. RecordObserved → d=2, o=3, a=2 (drifted: o!=a)
	//
	// El observer NUNCA debería incrementar observed antes de que el
	// reconciler haya aplicado al menos una vez — sería ruido.

	// SetCertAutoRenew → desired pasa a 2
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.SetCertAutoRenew(context.Background(), tx, cert.ID, false)
	})

	// MarkCertApplied → applied=desired=2, observed=desired=2 (sync)
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.MarkCertApplied(context.Background(), tx, cert.ID)
	})

	gotMid, _ := repo.GetCert(context.Background(), cert.ID)
	if gotMid.Convergence != (Convergence{Desired: 2, Observed: 2, Applied: 2}) {
		t.Errorf("post-apply convergence = %+v, want {2,2,2}", gotMid.Convergence)
	}
	if !gotMid.Convergence.IsConverged() {
		t.Error("post-apply: IsConverged should be true")
	}
	if gotMid.Convergence.HasDrifted() {
		t.Error("post-apply: HasDrifted should be false (just applied, no real divergence)")
	}

	// RecordCertObserved → observer detectó drift real → observed=3
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.RecordCertObserved(context.Background(), tx, cert.ID)
	})

	got, _ := repo.GetCert(context.Background(), cert.ID)
	if got.Convergence != (Convergence{Desired: 2, Observed: 3, Applied: 2}) {
		t.Errorf("convergence = %+v, want {2,3,2}", got.Convergence)
	}
	if !got.Convergence.IsConverged() {
		t.Error("IsConverged should be true (applied==desired)")
	}
	if !got.Convergence.HasDrifted() {
		t.Error("HasDrifted should be true (observed != applied)")
	}
}

func TestDdns_RecordObservedAndListDrifted(t *testing.T) {
	repo, _, c, cleanup := newTestRepo(t)
	defer cleanup()
	s1 := seedSecret(t, c.db, "a")
	s2 := seedSecret(t, c.db, "b")

	// d1 will be drifted; d2 won't.
	d1 := &NetworkDdns{Provider: "duckdns", Domain: "drift.duckdns.org", TokenSecretID: s1, Enabled: true}
	d2 := &NetworkDdns{Provider: "duckdns", Domain: "clean.duckdns.org", TokenSecretID: s2, Enabled: true}
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.CreateDdns(context.Background(), tx, d1); err != nil {
			return err
		}
		return repo.CreateDdns(context.Background(), tx, d2)
	})

	// Mark both applied (so observed != applied is meaningful)
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.MarkDdnsApplied(context.Background(), tx, d1.ID); err != nil {
			return err
		}
		return repo.MarkDdnsApplied(context.Background(), tx, d2.ID)
	})

	// Drift only d1
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.RecordDdnsObserved(context.Background(), tx, d1.ID)
	})

	got, err := repo.ListDriftedDdns(context.Background())
	if err != nil {
		t.Fatalf("ListDriftedDdns: %v", err)
	}
	if len(got) != 1 || got[0].Domain != "drift.duckdns.org" {
		t.Errorf("drifted = %v, want only [drift.duckdns.org]", ddnsDomains(got))
	}

	// Sanity: RecordDdnsObserved on non-existent → NotFound
	tx, _ := c.db.Begin()
	defer tx.Rollback()
	err = repo.RecordDdnsObserved(context.Background(), tx, "does-not-exist")
	if !errors.Is(err, ErrDdnsNotFound) {
		t.Errorf("err = %v, want ErrDdnsNotFound", err)
	}
}

func TestCerts_ListAllAndConvergenceQueries(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	mk := func(domain string) *NetworkCert {
		return &NetworkCert{
			Domain: domain, CertProvider: "selfsigned",
			FullchainPath: "/a", PrivkeyPath: "/b",
			NotBefore: clock.Now().UTC(), NotAfter: clock.Now().Add(365 * 24 * time.Hour).UTC(),
			Enabled: true, AutoRenew: true,
		}
	}

	c1, c2, c3 := mk("alpha.local"), mk("mike.local"), mk("zeta.local")
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.CreateCert(context.Background(), tx, c1); err != nil {
			return err
		}
		if err := repo.CreateCert(context.Background(), tx, c2); err != nil {
			return err
		}
		return repo.CreateCert(context.Background(), tx, c3)
	})

	// ListCerts: sorted by domain, all 3.
	all, err := repo.ListCerts(context.Background())
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListCerts got %d, want 3", len(all))
	}
	wantOrder := []string{"alpha.local", "mike.local", "zeta.local"}
	for i, c := range all {
		if c.Domain != wantOrder[i] {
			t.Errorf("[%d] = %s, want %s", i, c.Domain, wantOrder[i])
		}
	}

	// Apply c2 and c3 → only c1 stays pending.
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.MarkCertApplied(context.Background(), tx, c2.ID); err != nil {
			return err
		}
		return repo.MarkCertApplied(context.Background(), tx, c3.ID)
	})

	pending, err := repo.ListPendingCerts(context.Background())
	if err != nil {
		t.Fatalf("ListPendingCerts: %v", err)
	}
	if len(pending) != 1 || pending[0].Domain != "alpha.local" {
		t.Errorf("pending = %v, want only [alpha.local]", certDomains(pending))
	}

	// Drift c3 (record observed once → observed=1, applied=1 → no drift; twice → observed=2, drift)
	withNetTx(t, c.db, func(tx *sql.Tx) error {
		if err := repo.RecordCertObserved(context.Background(), tx, c3.ID); err != nil {
			return err
		}
		return repo.RecordCertObserved(context.Background(), tx, c3.ID)
	})

	drifted, err := repo.ListDriftedCerts(context.Background())
	if err != nil {
		t.Fatalf("ListDriftedCerts: %v", err)
	}
	if len(drifted) != 1 || drifted[0].Domain != "zeta.local" {
		t.Errorf("drifted = %v, want only [zeta.local]", certDomains(drifted))
	}
}

func TestCerts_DeleteAndNotFound(t *testing.T) {
	repo, clock, c, cleanup := newTestRepo(t)
	defer cleanup()

	cert := &NetworkCert{
		Domain: "host.local", CertProvider: "selfsigned",
		FullchainPath: "/a", PrivkeyPath: "/b",
		NotBefore: clock.Now().UTC(), NotAfter: clock.Now().Add(time.Hour).UTC(),
		Enabled: true,
	}
	withNetTx(t, c.db, func(tx *sql.Tx) error { return repo.CreateCert(context.Background(), tx, cert) })

	withNetTx(t, c.db, func(tx *sql.Tx) error {
		return repo.DeleteCert(context.Background(), tx, cert.ID)
	})

	_, err := repo.GetCert(context.Background(), cert.ID)
	if !errors.Is(err, ErrCertNotFound) {
		t.Errorf("err = %v, want ErrCertNotFound", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End of file
// ─────────────────────────────────────────────────────────────────────────────
