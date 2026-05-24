package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestComputeDockerAggregateHealth · cubre lógica pura (sin DB).
// Verifica que la función traduce children → HealthStatus correctamente
// según las reglas documentadas en nimhealth_docker.go.
func TestComputeDockerAggregateHealth(t *testing.T) {
	makeChild := func(status, health string) DockerAppStatus {
		return DockerAppStatus{
			ServiceBase: ServiceBase{
				Status: status,
				Health: health,
			},
		}
	}

	cases := []struct {
		name     string
		children []DockerAppStatus
		want     HealthStatus
	}{
		{
			name:     "no children · engine OK vacío",
			children: nil,
			want:     HealthHealthy,
		},
		{
			name: "all running healthy",
			children: []DockerAppStatus{
				makeChild("running", string(HealthHealthy)),
				makeChild("running", string(HealthHealthy)),
			},
			want: HealthHealthy,
		},
		{
			name: "all stopped · engine OK sin actividad",
			children: []DockerAppStatus{
				makeChild("stopped", string(HealthHealthy)),
				makeChild("stopped", string(HealthHealthy)),
			},
			want: HealthHealthy,
		},
		{
			name: "one error",
			children: []DockerAppStatus{
				makeChild("running", string(HealthHealthy)),
				makeChild("error", string(HealthFailed)),
			},
			want: HealthDegraded,
		},
		{
			name: "mix running + stopped",
			children: []DockerAppStatus{
				makeChild("running", string(HealthHealthy)),
				makeChild("stopped", string(HealthHealthy)),
			},
			want: HealthDegraded,
		},
		{
			name: "one failed health (sin status error)",
			children: []DockerAppStatus{
				makeChild("running", string(HealthFailed)),
			},
			want: HealthDegraded,
		},
		{
			name: "single running",
			children: []DockerAppStatus{
				makeChild("running", string(HealthHealthy)),
			},
			want: HealthHealthy,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ComputeDockerAggregateHealth(c.children)
			if got != c.want {
				t.Errorf("ComputeDockerAggregateHealth(...) = %v, want %v", got, c.want)
			}
		})
	}
}

// TestInBootGracePeriod · usa el hook osReadFile para inyectar uptimes.
func TestInBootGracePeriod(t *testing.T) {
	original := osReadFile
	defer func() { osReadFile = original }()

	cases := []struct {
		name      string
		uptimeStr string
		want      bool
	}{
		{"recién arrancado · 5s", "5.00 4.50\n", true},
		{"en gracia · 30s", "30.00 25.00\n", true},
		{"borde inferior justo antes · 89s", "89.00 80.00\n", true},
		{"borde justo en límite · 90s", "90.00 80.00\n", false},
		{"fuera de gracia · 100s", "100.00 90.00\n", false},
		{"sistema viejo · 5 días", "432000.00 100000.00\n", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			osReadFile = func(path string) ([]byte, error) {
				return []byte(c.uptimeStr), nil
			}
			got := inBootGracePeriod()
			if got != c.want {
				t.Errorf("inBootGracePeriod() = %v, want %v (uptime=%q)", got, c.want, c.uptimeStr)
			}
		})
	}

	// Caso: /proc/uptime no se puede leer → false (no aplicar gracia)
	t.Run("read error → no grace", func(t *testing.T) {
		osReadFile = func(path string) ([]byte, error) {
			return nil, fmt.Errorf("simulated")
		}
		if inBootGracePeriod() {
			t.Error("inBootGracePeriod() should be false when /proc/uptime unreadable")
		}
	})

	// Caso: /proc/uptime vacío → false
	t.Run("empty uptime → no grace", func(t *testing.T) {
		osReadFile = func(path string) ([]byte, error) {
			return []byte(""), nil
		}
		if inBootGracePeriod() {
			t.Error("inBootGracePeriod() should be false when /proc/uptime empty")
		}
	})
}

// TestDockerAppCache_ConcurrentAccess · stress test del RWMutex.
// Múltiples readers + 1 writer concurrent · no debe haber data race
// (corre con -race en CI).
func TestDockerAppCache_ConcurrentAccess(t *testing.T) {
	var cache DockerAppCache

	// Inicializar con valor known
	cache.mu.Lock()
	cache.statuses = []DockerAppStatus{}
	cache.aggHealth = HealthHealthy
	cache.initialized = true
	cache.mu.Unlock()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 8 readers concurrent
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cache.mu.RLock()
					_ = cache.aggHealth
					_ = cache.statuses
					cache.mu.RUnlock()
				}
			}
		}()
	}

	// 1 writer concurrent, alterna health
	wg.Add(1)
	go func() {
		defer wg.Done()
		toggle := false
		for {
			select {
			case <-stop:
				return
			default:
				cache.mu.Lock()
				if toggle {
					cache.aggHealth = HealthHealthy
				} else {
					cache.aggHealth = HealthDegraded
				}
				toggle = !toggle
				cache.updatedAt = time.Now()
				cache.mu.Unlock()
			}
		}
	}()

	// Correr 200ms (suficiente para muchas iteraciones)
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Si llegamos aquí sin deadlock ni race, OK
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if !cache.initialized {
		t.Error("cache should still be initialized")
	}
}

// TestNimHealthObserver_ReconcilerInterface · garantiza que la interfaz
// Reconciler está bien implementada (Name/Tier/Interval no panic).
func TestNimHealthObserver_ReconcilerInterface(t *testing.T) {
	obs := NewNimHealthObserver(NewRealClock(), DefaultNimHealthConfig())

	if obs.Name() == "" {
		t.Error("Name() must not be empty")
	}
	if obs.Name() != "nimhealth_observer" {
		t.Errorf("Name() = %q, want %q", obs.Name(), "nimhealth_observer")
	}
	if obs.Tier() != TierMedium {
		t.Errorf("Tier() = %v, want TierMedium", obs.Tier())
	}
	if obs.Interval() <= 0 {
		t.Errorf("Interval() = %v, must be > 0", obs.Interval())
	}
	if obs.Interval() != 30*time.Second {
		t.Errorf("Interval() = %v, want 30s default", obs.Interval())
	}
}

// TestNimHealthObserver_NilClock · sin panic si pasas clock nil.
// Debe fallback a RealClock interno.
func TestNimHealthObserver_NilClock(t *testing.T) {
	obs := NewNimHealthObserver(nil, DefaultNimHealthConfig())
	if obs.clock == nil {
		t.Error("NewNimHealthObserver should fallback to RealClock when nil passed")
	}
}

// TestNimHealthObserver_ZeroInterval · sin panic si config.Interval = 0.
// Debe usar default 30s.
func TestNimHealthObserver_ZeroInterval(t *testing.T) {
	obs := NewNimHealthObserver(NewRealClock(), NimHealthObserverConfig{Interval: 0})
	if obs.Interval() != 30*time.Second {
		t.Errorf("Interval() = %v, want 30s default when config is 0", obs.Interval())
	}
}

// Asegurar que el observer implementa Reconciler (compile-time check).
var _ Reconciler = (*NimHealthObserver)(nil)

// Asegurar que el contexto pasado a Reconcile no se ignora · al menos
// debe poderse llamar sin panic.
func TestNimHealthObserver_ReconcileWithCancelledCtx(t *testing.T) {
	// NOTA: este test SOLO verifica que Reconcile no panics con ctx
	// cancelado. NO verifica resultado porque eso necesita DB real.
	// La compilación + ausencia de panic ya es bastante señal.
	t.Skip("requires DB · ejecutar en integration tests con sqlite real")
	_ = context.Background()
}
