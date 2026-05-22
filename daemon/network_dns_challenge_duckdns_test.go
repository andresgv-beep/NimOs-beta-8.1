// network_dns_challenge_duckdns_test.go — Tests del DuckDNSChallengeProvider.

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// dnsChallengeServer monta un httptest server que captura las requests.
func dnsChallengeServer(t *testing.T, response string, statusCode int) (*httptest.Server, *struct {
	mu      sync.Mutex
	calls   []capturedDNSCall
}) {
	t.Helper()
	captured := &struct {
		mu    sync.Mutex
		calls []capturedDNSCall
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.mu.Lock()
		captured.calls = append(captured.calls, capturedDNSCall{
			Domains: r.URL.Query().Get("domains"),
			Token:   r.URL.Query().Get("token"),
			TXT:     r.URL.Query().Get("txt"),
			Clear:   r.URL.Query().Get("clear"),
		})
		captured.mu.Unlock()
		w.WriteHeader(statusCode)
		w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

type capturedDNSCall struct {
	Domains string
	Token   string
	TXT     string
	Clear   string
}

func buildDNSChallengeProvider(t *testing.T, endpoint, token string) *DuckDNSChallengeProvider {
	t.Helper()
	breaker := NewCircuitBreaker(DefaultBreakerConfig("dns-challenge-test"))
	p, err := NewDuckDNSChallengeProvider(DuckDNSChallengeProviderConfig{
		Token:    token,
		Breaker:  breaker,
		Endpoint: endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ═════════════════════════════════════════════════════════════════════════════
// Construction
// ═════════════════════════════════════════════════════════════════════════════

func TestDuckDNSChallenge_RequiresToken(t *testing.T) {
	_, err := NewDuckDNSChallengeProvider(DuckDNSChallengeProviderConfig{
		Breaker: NewCircuitBreaker(DefaultBreakerConfig("x")),
	})
	if err == nil {
		t.Error("expected error without Token")
	}
}

func TestDuckDNSChallenge_RequiresBreaker(t *testing.T) {
	_, err := NewDuckDNSChallengeProvider(DuckDNSChallengeProviderConfig{Token: "x"})
	if err == nil {
		t.Error("expected error without Breaker")
	}
}

func TestDuckDNSChallenge_NameIsStable(t *testing.T) {
	p := buildDNSChallengeProvider(t, defaultDuckDNSEndpoint, "tok")
	if p.Name() != "duckdns" {
		t.Errorf("Name = %q, want duckdns", p.Name())
	}
}

func TestDuckDNSChallenge_DefaultsApplied(t *testing.T) {
	p, err := NewDuckDNSChallengeProvider(DuckDNSChallengeProviderConfig{
		Token:   "tok",
		Breaker: NewCircuitBreaker(DefaultBreakerConfig("x")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.httpClient == nil || p.httpClient.Timeout == 0 {
		t.Error("default http client not applied")
	}
	if p.endpoint != defaultDuckDNSEndpoint {
		t.Errorf("endpoint default = %q, want %q", p.endpoint, defaultDuckDNSEndpoint)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// SetTXT
// ═════════════════════════════════════════════════════════════════════════════

func TestDuckDNSChallenge_SetTXTSuccess(t *testing.T) {
	srv, captured := dnsChallengeServer(t, "OK", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "secret-tok")

	err := p.SetTXT(context.Background(), "test.duckdns.org", "challenge-value-123")
	if err != nil {
		t.Fatalf("SetTXT: %v", err)
	}

	captured.mu.Lock()
	defer captured.mu.Unlock()
	if len(captured.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(captured.calls))
	}
	c := captured.calls[0]
	if c.Domains != "test" {
		t.Errorf("domains = %q, want test", c.Domains)
	}
	if c.Token != "secret-tok" {
		t.Errorf("token = %q", c.Token)
	}
	if c.TXT != "challenge-value-123" {
		t.Errorf("txt = %q, want challenge-value-123", c.TXT)
	}
	if c.Clear == "true" {
		t.Error("SetTXT should NOT include clear=true")
	}
}

func TestDuckDNSChallenge_SetTXTRejectsEmptyValue(t *testing.T) {
	srv, captured := dnsChallengeServer(t, "OK", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	if err := p.SetTXT(context.Background(), "test", ""); err == nil {
		t.Error("expected error for empty value")
	}
	captured.mu.Lock()
	defer captured.mu.Unlock()
	if len(captured.calls) != 0 {
		t.Error("should not call server on validation failure")
	}
}

func TestDuckDNSChallenge_SetTXTRejectsInvalidDomain(t *testing.T) {
	srv, _ := dnsChallengeServer(t, "OK", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	for _, bad := range []string{"", "has space", "-bad", "bad-"} {
		if err := p.SetTXT(context.Background(), bad, "val"); err == nil {
			t.Errorf("domain %q: expected error", bad)
		}
	}
}

func TestDuckDNSChallenge_SetTXT_KOIsChallengeFailed(t *testing.T) {
	srv, _ := dnsChallengeServer(t, "KO", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "wrong-tok")

	err := p.SetTXT(context.Background(), "test", "val")
	if !errors.Is(err, ErrCertChallengeFailed) {
		t.Errorf("err = %v, want ErrCertChallengeFailed", err)
	}
}

func TestDuckDNSChallenge_KOdoesNotOpenBreaker(t *testing.T) {
	srv, _ := dnsChallengeServer(t, "KO", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "wrong-tok")

	for i := 0; i < 20; i++ {
		_ = p.SetTXT(context.Background(), "test", "val")
	}
	if state := p.breaker.GetState(); state != CircuitClosed {
		t.Errorf("breaker = %v after many KO, want closed", state)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// RemoveTXT
// ═════════════════════════════════════════════════════════════════════════════

func TestDuckDNSChallenge_RemoveTXTSendsClearParam(t *testing.T) {
	srv, captured := dnsChallengeServer(t, "OK", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	if err := p.RemoveTXT(context.Background(), "test.duckdns.org"); err != nil {
		t.Fatal(err)
	}

	captured.mu.Lock()
	defer captured.mu.Unlock()
	if len(captured.calls) != 1 {
		t.Fatal("expected 1 call")
	}
	c := captured.calls[0]
	if c.Clear != "true" {
		t.Errorf("clear = %q, want true", c.Clear)
	}
	if c.TXT != "removed" {
		t.Errorf("txt = %q, want 'removed'", c.TXT)
	}
}

func TestDuckDNSChallenge_RemoveTXTIdempotent(t *testing.T) {
	// El server siempre devuelve OK → RemoveTXT debe ser segura para
	// llamar múltiples veces.
	srv, captured := dnsChallengeServer(t, "OK", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	for i := 0; i < 3; i++ {
		if err := p.RemoveTXT(context.Background(), "test"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	captured.mu.Lock()
	if len(captured.calls) != 3 {
		t.Errorf("expected 3 calls, got %d", len(captured.calls))
	}
	captured.mu.Unlock()
}

// ═════════════════════════════════════════════════════════════════════════════
// Breaker integration
// ═════════════════════════════════════════════════════════════════════════════

func TestDuckDNSChallenge_TransientOpensBreaker(t *testing.T) {
	srv, _ := dnsChallengeServer(t, "boom", http.StatusInternalServerError)
	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	threshold := DefaultBreakerConfig("x").FailureThreshold
	for i := 0; i < threshold+2; i++ {
		_ = p.SetTXT(context.Background(), "test", "val")
	}
	if state := p.breaker.GetState(); state != CircuitOpen {
		t.Errorf("breaker = %v after transient failures, want open", state)
	}
}

func TestDuckDNSChallenge_OpenBreakerReturnsTransient(t *testing.T) {
	srv, _ := dnsChallengeServer(t, "boom", http.StatusInternalServerError)
	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	threshold := DefaultBreakerConfig("x").FailureThreshold
	for i := 0; i < threshold+1; i++ {
		_ = p.SetTXT(context.Background(), "test", "val")
	}
	err := p.SetTXT(context.Background(), "test", "val")
	if !errors.Is(err, ErrCertProviderTransient) {
		t.Errorf("err = %v, want ErrCertProviderTransient (breaker open)", err)
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Misc
// ═════════════════════════════════════════════════════════════════════════════

func TestDuckDNSChallenge_TokenNotLeaked(t *testing.T) {
	srv, _ := dnsChallengeServer(t, "weird response", http.StatusOK)
	p := buildDNSChallengeProvider(t, srv.URL, "supersecret-token-12345")

	err := p.SetTXT(context.Background(), "test", "val")
	if err == nil {
		t.Fatal("expected error for weird response")
	}
	if strings.Contains(err.Error(), "supersecret-token-12345") {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestDuckDNSChallenge_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.Write([]byte("OK"))
	}))
	defer srv.Close()

	p := buildDNSChallengeProvider(t, srv.URL, "tok")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := p.SetTXT(ctx, "test", "val")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if elapsed > time.Second {
		t.Errorf("took %v, expected fast cancel", elapsed)
	}
}
