# NIMOS NETWORK MODULE — Plan de desarrollo v3

**Versión**: 3 (revisión arquitectónica completa de nivel senior)
**Estado**: PENDIENTE · próximo módulo grande tras Storage Beta 8.1
**Cambios v2 → v3**: aplicada segunda ronda de crítica arquitectónica de Andrés

---

## CAMBIOS FUNDAMENTALES v2 → v3

```
v2                                       v3 (mejorado)
─────────────────────────────────────────────────────────────────────
ObservedSnapshot en memoria             ObservedSnapshot + persistido en SQLite
Tokens DDNS texto plano (Beta 9+)       AES-GCM at-rest desde F-001
network_operations básico               + triggered_by + request_id (correlation)
Asume certbot/openssl instalados        Capability detection formal
Sin circuit breaker para providers      BreakerProvider wrapper
Reconcilers iguales                     Priority tiers (critical/medium/low)
Generation single                       Desired/Observed/Applied (3 generations)
Events sin category                     Events con category + event indexados
Cada componente health diferente        HealthStatus enum unificado
Declared/Observed/Reconcile             Declared/Observed/Reconcile + Convergence
```

---

## PATRONES TRANSVERSALES (aplicables a TODO NimOS, no solo Network)

### 1. Health Status unificado

```go
package nimos

// HealthStatus es el enum único de salud para CUALQUIER entidad
// observable en NimOS: pools, certs, ddns, listeners, UPnP, providers, etc.
//
// Aplicar consistentemente unifica:
//   · UI dashboard (mismo badge en todas partes)
//   · Analytics agregables ("cuántas entidades degraded")
//   · Health overall = MAX(severity de componentes)
//   · Alerts y notificaciones (tratan todo igual)
type HealthStatus string

const (
    HealthHealthy  HealthStatus = "healthy"   // funciona como se espera
    HealthDegraded HealthStatus = "degraded"  // funciona pero con problemas
    HealthFailed   HealthStatus = "failed"    // no funciona
    HealthPartial  HealthStatus = "partial"   // funciona parcialmente (algunos OK, otros KO)
    HealthUnknown  HealthStatus = "unknown"   // no se pudo determinar
    HealthStale    HealthStatus = "stale"     // datos viejos, no refrescados recientemente
)

// Severity numérica para comparación / aggregation
func (h HealthStatus) Severity() int {
    switch h {
    case HealthHealthy:  return 0
    case HealthStale:    return 1
    case HealthUnknown:  return 2
    case HealthDegraded: return 3
    case HealthPartial:  return 4
    case HealthFailed:   return 5
    default:             return 6
    }
}

// HealthAggregate de varios HealthStatus → el peor
func HealthAggregate(statuses ...HealthStatus) HealthStatus {
    worst := HealthHealthy
    for _, s := range statuses {
        if s.Severity() > worst.Severity() {
            worst = s
        }
    }
    return worst
}
```

### 2. Triple Generation (Declared / Observed / Applied)

```go
// Cada entidad reconciable tiene 3 generations:
//
//   DesiredGeneration  = lo que el usuario/sistema DECLARA querer
//                        (incrementa al cambiar config en DB)
//
//   ObservedGeneration = lo que el sistema VE realmente ahora
//                        (incrementa al detectar cambio en runtime)
//
//   AppliedGeneration  = lo que el reconciler APLICÓ con éxito
//                        (incrementa al converger correctamente)
//
// Convergencia: AppliedGeneration == DesiredGeneration
// Drift:        ObservedGeneration != AppliedGeneration
// Pendiente:    AppliedGeneration < DesiredGeneration
//
// Esto es lo que Kubernetes y systemd hacen conceptualmente.

type Convergence struct {
    Desired  int64 `json:"desired_generation"`
    Observed int64 `json:"observed_generation"`
    Applied  int64 `json:"applied_generation"`
}

func (c Convergence) IsConverged() bool {
    return c.Applied == c.Desired
}

func (c Convergence) HasDrifted() bool {
    return c.Observed != c.Applied
}

func (c Convergence) IsPending() bool {
    return c.Applied < c.Desired
}
```

### 3. Capability Detection

```go
// Capabilities del runtime — qué tools tiene el sistema realmente.
// Se detecta al boot y se cachea, se refresca cada N minutos.

type SystemCapabilities struct {
    // Network
    CertbotInstalled bool      `json:"certbot_installed"`
    CertbotVersion   string    `json:"certbot_version,omitempty"`
    OpenSSLInstalled bool      `json:"openssl_installed"`
    UPnPClient       bool      `json:"upnp_client"`         // upnpc / miniupnpc / via lib
    NFTBackend       bool      `json:"nft_backend"`         // nftables
    IPTablesBackend  bool      `json:"iptables_backend"`    // iptables (legacy)
    UFWInstalled     bool      `json:"ufw_installed"`
    
    // DNS
    DigInstalled     bool      `json:"dig_installed"`
    HostInstalled    bool      `json:"host_installed"`
    
    // Misc
    SystemdAvailable bool      `json:"systemd_available"`
    
    DetectedAt       time.Time `json:"detected_at"`
}

func DetectSystemCapabilities() *SystemCapabilities {
    return &SystemCapabilities{
        CertbotInstalled: commandExists("certbot"),
        OpenSSLInstalled: commandExists("openssl"),
        UPnPClient:       commandExists("upnpc"),
        NFTBackend:       commandExists("nft"),
        IPTablesBackend:  commandExists("iptables"),
        UFWInstalled:     commandExists("ufw"),
        DigInstalled:     commandExists("dig"),
        HostInstalled:    commandExists("host"),
        SystemdAvailable: pathExists("/run/systemd/system"),
        DetectedAt:       time.Now().UTC(),
    }
}

// El observer expone estas capabilities. La UI las muestra:
// "Cert DNS-01 no disponible: falta certbot. Instalar:"
//    sudo apt install certbot
```

### 4. Provider Circuit Breaker

```go
// BreakerProvider wraps cualquier provider con circuit breaker.
// Pattern Hystrix simplificado.

type CircuitState string

const (
    CircuitClosed   CircuitState = "closed"    // funciona normal
    CircuitOpen     CircuitState = "open"      // demasiados fallos, cooldown
    CircuitHalfOpen CircuitState = "half_open" // intentando recovery
)

type CircuitBreaker struct {
    Name              string
    FailureThreshold  int           // ej. 5 fallos consecutivos
    CooldownDuration  time.Duration // ej. 5 min antes de half-open
    HalfOpenMaxCalls  int           // ej. 1 call en half-open
    
    mu          sync.Mutex
    state       CircuitState
    failures    int
    lastFailure time.Time
    nextRetry   time.Time
}

func (b *CircuitBreaker) Call(fn func() error) error {
    b.mu.Lock()
    if b.state == CircuitOpen && time.Now().Before(b.nextRetry) {
        b.mu.Unlock()
        return ErrCircuitOpen
    }
    if b.state == CircuitOpen {
        b.state = CircuitHalfOpen
    }
    b.mu.Unlock()
    
    err := fn()
    
    b.mu.Lock()
    defer b.mu.Unlock()
    
    if err != nil {
        b.failures++
        b.lastFailure = time.Now()
        if b.failures >= b.FailureThreshold {
            b.state = CircuitOpen
            b.nextRetry = time.Now().Add(b.CooldownDuration)
        }
        return err
    }
    
    // success
    b.failures = 0
    b.state = CircuitClosed
    return nil
}

// Aplicación:
duckDNSBreaker := &CircuitBreaker{
    Name: "duckdns",
    FailureThreshold: 5,
    CooldownDuration: 5 * time.Minute,
}

duckDNSBreaker.Call(func() error {
    return duckDNSProvider.PublishTXT(ctx, name, value)
})
```

### 5. Reconciler Priority Tiers

```go
type ReconcilerTier string

const (
    TierCritical ReconcilerTier = "critical" // cada minuto
    TierHigh     ReconcilerTier = "high"     // cada 5 min
    TierMedium   ReconcilerTier = "medium"   // cada 15 min
    TierLow      ReconcilerTier = "low"      // cada hora
)

type NamedReconciler struct {
    Name       string
    Reconciler Reconciler
    Tier       ReconcilerTier
    // Si tier es Critical y hay drift, ejecutar AHORA sin esperar interval
    ForceOnDrift bool
}

// Asignaciones (Network):
//   cert_renewer  → Critical   (expiración crítica)
//   port_listener → Critical   (puerto del daemon no escucha = NimOS down)
//   ddns_updater  → Medium     (15min suficiente)
//   upnp_refresh  → Low        (cada hora)
//   capability_detect → Low    (poco cambia)
```

---

## ARQUITECTURA NETWORK v3

```
┌─────────────────────────────────────────────────────────────┐
│ CAPA 7 · HTTP API                                           │
└────────────────────┬────────────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────────────┐
│ CAPA 6 · SERVICE + POLICY + CONVERGENCE                     │
└─┬─────────────────┬─────────────────┬───────────────────┬───┘
  │                 │                 │                   │
┌─▼───────────────┐ │ ┌───────────────▼─┐ ┌───────────────▼─┐
│ DDNSReconciler  │ │ │ CertReconciler  │ │ PortReconciler  │
│ (TIER: Medium)  │ │ │ (TIER: Critical)│ │ (TIER: Critical)│
└─┬───────────────┘ │ └────────┬────────┘ └────────┬────────┘
  │                 │          │                   │
  │                 │  ┌───────▼─────────────┐     │
  │                 │  │ CertProvider +      │     │
  │                 │  │ DNSChallengeProvider│     │
  │                 │  │ (wrapped en Breaker)│     │
  │                 │  └───────┬─────────────┘     │
  │                 │          │                   │
┌─▼─────────────────▼──────────▼───────────────────▼──────────┐
│ CAPA 5 · OBSERVER + HEALTH + CAPABILITIES                   │
│ NetworkObservedSnapshot (memoria + persistido en SQLite)    │
│ SystemCapabilities (detectado al boot)                      │
│ HealthStatus enum unificado                                 │
└────────────────────┬────────────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────────────┐
│ CAPA 4 · BACKENDS (con capability awareness)                │
└────────────────────┬────────────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────────────┐
│ CAPA 3 · EXECUTOR (mockable)                                │
└────────────────────┬────────────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────────────┐
│ CAPA 2 · TIPOS + UTILITIES                                  │
│ + HealthStatus + Convergence + SystemCapabilities + Breaker │
└────────────────────┬────────────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────────────┐
│ CAPA 1 · SCHEMA + REPO + ENCRYPTION                         │
│ network_* + nimos_secrets (AES-GCM)                         │
└─────────────────────────────────────────────────────────────┘
```

---

## SCHEMA SQLITE v3

```sql
-- =============================================================================
-- network_ports — Puertos del daemon (con triple generation)
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_ports (
    id                   TEXT    PRIMARY KEY,        -- 'http', 'https'
    port                 INTEGER NOT NULL CHECK(port > 0 AND port < 65536),
    bind_address         TEXT    NOT NULL DEFAULT '0.0.0.0',
    enabled              INTEGER NOT NULL DEFAULT 1,
    
    desired_generation   INTEGER NOT NULL DEFAULT 0 CHECK(desired_generation >= 0),
    observed_generation  INTEGER NOT NULL DEFAULT 0 CHECK(observed_generation >= 0),
    applied_generation   INTEGER NOT NULL DEFAULT 0 CHECK(applied_generation >= 0),
    
    updated_at           TEXT    NOT NULL
);

-- =============================================================================
-- network_ddns — DDNS config (con tokens encrypted)
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_ddns (
    id                   TEXT    PRIMARY KEY,
    provider             TEXT    NOT NULL CHECK(provider IN ('duckdns','noip','dynu','freedns','cloudflare')),
    domain               TEXT    NOT NULL,
    
    -- Token SIEMPRE encrypted with AES-GCM (network_secrets table)
    token_secret_id      TEXT    NOT NULL,    -- FK a nimos_secrets.id
    
    enabled              INTEGER NOT NULL DEFAULT 0,
    auto_update          INTEGER NOT NULL DEFAULT 1,
    update_interval      INTEGER NOT NULL DEFAULT 900,
    
    last_run_at          TEXT,
    last_run_result      TEXT,
    last_ip              TEXT,
    
    desired_generation   INTEGER NOT NULL DEFAULT 0,
    observed_generation  INTEGER NOT NULL DEFAULT 0,
    applied_generation   INTEGER NOT NULL DEFAULT 0,
    
    FOREIGN KEY (token_secret_id) REFERENCES nimos_secrets(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ddns_provider ON network_ddns(provider);

-- =============================================================================
-- nimos_secrets — Tabla GLOBAL de secretos encrypted (no solo network)
-- =============================================================================
-- AES-GCM con key en /var/lib/nimos/keys/master.key (chmod 600 nimos:nimos)
-- Rotation future: campo key_version permite migrar gradualmente
CREATE TABLE IF NOT EXISTS nimos_secrets (
    id              TEXT    PRIMARY KEY,
    category        TEXT    NOT NULL,        -- 'ddns_token', 'api_key', 'ssh_key', etc.
    label           TEXT    NOT NULL,        -- 'DuckDNS token for nimosbarraca1'
    
    ciphertext      BLOB    NOT NULL,        -- AES-GCM encrypted
    nonce           BLOB    NOT NULL,        -- nonce único por secret
    key_version     INTEGER NOT NULL DEFAULT 1,
    
    created_at      TEXT    NOT NULL,
    last_accessed   TEXT,
    
    UNIQUE(category, label)
);

CREATE INDEX IF NOT EXISTS idx_secrets_category ON nimos_secrets(category);

-- =============================================================================
-- network_certs — con convergence
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_certs (
    id                   TEXT    PRIMARY KEY,
    domain               TEXT    NOT NULL UNIQUE,
    
    cert_provider        TEXT    NOT NULL
        CHECK(cert_provider IN ('letsencrypt','letsencrypt_staging','zerossl','selfsigned')),
    challenge_type       TEXT
        CHECK(challenge_type IS NULL OR challenge_type IN ('http-01','dns-01')),
    dns_provider         TEXT
        CHECK(dns_provider IS NULL OR dns_provider IN ('duckdns','cloudflare','route53','dynu','porkbun')),
    
    fullchain_path       TEXT    NOT NULL,
    privkey_path         TEXT    NOT NULL,
    
    not_before           TEXT    NOT NULL,
    not_after            TEXT    NOT NULL,
    
    enabled              INTEGER NOT NULL DEFAULT 1,
    auto_renew           INTEGER NOT NULL DEFAULT 1,
    
    issued_at            TEXT    NOT NULL,
    last_renewed_at      TEXT,
    
    desired_generation   INTEGER NOT NULL DEFAULT 0,
    observed_generation  INTEGER NOT NULL DEFAULT 0,
    applied_generation   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_certs_domain     ON network_certs(domain);
CREATE INDEX IF NOT EXISTS idx_certs_not_after  ON network_certs(not_after);
CREATE INDEX IF NOT EXISTS idx_certs_auto_renew ON network_certs(auto_renew) WHERE auto_renew = 1;

-- =============================================================================
-- network_observed — Snapshots históricos persistidos
-- =============================================================================
-- Permite:
--   · Recovery tras crash (último estado conocido)
--   · Debugging histórico ("¿qué pasaba a las 03:42?")
--   · Diff de snapshots
--   · Boot diagnostics
--   · Métricas a largo plazo
--
-- Retención: 100 últimos snapshots + uno por día durante último mes
CREATE TABLE IF NOT EXISTS network_observed (
    id              TEXT    PRIMARY KEY,
    generation      INTEGER NOT NULL,
    snapshot_at     TEXT    NOT NULL,         -- ISO 8601
    
    -- Snapshot serializado completo (JSON)
    snapshot_data   TEXT    NOT NULL,
    
    -- Métricas indexadas para queries rápidas sin parsear JSON
    overall_health  TEXT    NOT NULL CHECK(overall_health IN ('healthy','degraded','failed','partial','unknown','stale')),
    public_ip       TEXT,
    ddns_synced     INTEGER,                  -- 0/1
    certs_total     INTEGER NOT NULL DEFAULT 0,
    certs_expiring  INTEGER NOT NULL DEFAULT 0,
    divergence_count INTEGER NOT NULL DEFAULT 0,
    
    scan_duration_ms INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_observed_at ON network_observed(snapshot_at DESC);
CREATE INDEX IF NOT EXISTS idx_observed_health ON network_observed(overall_health);
CREATE INDEX IF NOT EXISTS idx_observed_gen ON network_observed(generation DESC);

-- =============================================================================
-- network_operations — con ownership formal
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_operations (
    id                TEXT    PRIMARY KEY,
    type              TEXT    NOT NULL,
    target_id         TEXT,
    status            TEXT    NOT NULL
        CHECK(status IN ('pending','in_progress','completed','failed','rolled_back')),
    
    -- OWNERSHIP / CORRELATION (nuevo en v3)
    triggered_by      TEXT    NOT NULL          -- 'user:admin', 'reconciler:ddns', 'system:boot'
        CHECK(triggered_by LIKE 'user:%' OR triggered_by LIKE 'reconciler:%' OR triggered_by = 'system:boot' OR triggered_by = 'system:scheduler'),
    request_id        TEXT,                      -- UUID de correlación (HTTP request, batch op, etc.)
    parent_operation  TEXT,                      -- si esta op fue triggered por otra
    
    started_at        TEXT    NOT NULL,
    completed_at      TEXT,
    error             TEXT,
    error_code        TEXT,
    data              TEXT,
    
    FOREIGN KEY (parent_operation) REFERENCES network_operations(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_net_ops_status     ON network_operations(status);
CREATE INDEX IF NOT EXISTS idx_net_ops_started    ON network_operations(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_net_ops_triggered  ON network_operations(triggered_by);
CREATE INDEX IF NOT EXISTS idx_net_ops_request    ON network_operations(request_id);

-- =============================================================================
-- network_events — con category indexada
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_events (
    id           TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    timestamp    TEXT NOT NULL,
    
    -- Filtrado eficiente: queries tipo "todos los ddns_update_failed"
    category     TEXT NOT NULL,                  -- 'ddns', 'cert', 'port', 'upnp'
    event        TEXT NOT NULL,                  -- 'update_started', 'update_failed', 'cert_issued'
    
    level        TEXT NOT NULL CHECK(level IN ('debug','info','warn','error')),
    message      TEXT NOT NULL,
    
    -- Detalles estructurados (JSON) para parsing y métricas
    details      TEXT,
    
    FOREIGN KEY (operation_id) REFERENCES network_operations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_events_operation  ON network_events(operation_id);
CREATE INDEX IF NOT EXISTS idx_events_timestamp  ON network_events(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_events_category   ON network_events(category, event);
CREATE INDEX IF NOT EXISTS idx_events_level      ON network_events(level);

-- =============================================================================
-- network_capabilities — Cache de detection (refresh periódico)
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_capabilities (
    id               TEXT PRIMARY KEY,            -- 'system' (singleton)
    detected_at      TEXT NOT NULL,
    capabilities     TEXT NOT NULL                -- JSON SystemCapabilities
);

-- =============================================================================
-- network_breakers — Estado de circuit breakers (persistido)
-- =============================================================================
CREATE TABLE IF NOT EXISTS network_breakers (
    name              TEXT    PRIMARY KEY,        -- 'duckdns', 'letsencrypt', 'ifconfig.me'
    state             TEXT    NOT NULL CHECK(state IN ('closed','open','half_open')),
    failures          INTEGER NOT NULL DEFAULT 0,
    last_failure_at   TEXT,
    next_retry_at     TEXT,
    total_calls       INTEGER NOT NULL DEFAULT 0,
    total_failures    INTEGER NOT NULL DEFAULT 0
);
```

---

## NETWORK OBSERVED CON HEALTH + CONVERGENCE

```go
type NetworkObservedSnapshot struct {
    Generation int64     `json:"generation"`
    Timestamp  time.Time `json:"timestamp"`
    
    // Capabilities del sistema (de network_capabilities)
    Capabilities *SystemCapabilities `json:"capabilities"`
    
    // Health agregado del módulo entero
    OverallHealth HealthStatus `json:"overall_health"`
    
    // IP pública (de múltiples sources)
    PublicIP *PublicIPObserved `json:"public_ip"`
    
    // DDNS state
    DDNS []DDNSObserved `json:"ddns"`
    
    // Certs
    Certs []CertObserved `json:"certs"`
    
    // Listeners reales
    Listeners []ListenerObserved `json:"listeners"`
    
    // UPnP del router (best-effort)
    Router *RouterObserved `json:"router,omitempty"`
    
    // Port forwards conocidos
    PortForwards []PortForwardObserved `json:"port_forwards"`
    
    // Breakers state
    Breakers []BreakerObserved `json:"breakers"`
    
    // Divergencias pre-computadas
    Divergences []NetworkDivergence `json:"divergences"`
    
    ScanDurationMs int64 `json:"scan_duration_ms"`
}

type DDNSObserved struct {
    Domain     string       `json:"domain"`
    Provider   string       `json:"provider"`
    ResolvesTo []string     `json:"resolves_to"`
    
    Health     HealthStatus `json:"health"`
    HealthReason string     `json:"health_reason,omitempty"`
    
    Convergence Convergence `json:"convergence"`
    LastChecked time.Time   `json:"last_checked"`
}

type CertObserved struct {
    Domain          string       `json:"domain"`
    Provider        string       `json:"provider"`
    DaysUntilExpiry int          `json:"days_until_expiry"`
    
    Health      HealthStatus `json:"health"`         // healthy / degraded (<30d) / failed (expired)
    HealthReason string      `json:"health_reason,omitempty"`
    
    Convergence Convergence `json:"convergence"`
    OnDisk      bool        `json:"on_disk"`
    InDB        bool        `json:"in_db"`
}

type BreakerObserved struct {
    Name           string       `json:"name"`
    State          CircuitState `json:"state"`
    Failures       int          `json:"failures"`
    NextRetry      *time.Time   `json:"next_retry,omitempty"`
    Health         HealthStatus `json:"health"`        // open → failed, half_open → degraded
}
```

---

## FEATURES (8 — re-arquitecturadas v3)

### F-001 — Schema SQLite + Repo + Reconciler base + AES-GCM ⭐ FUNDACIÓN

**Coste**: ~7h (era 5h, +2h por AES-GCM)
**Outputs**:
- `network_schema.sql` con 9 tablas (+ network_observed, capabilities, breakers, secrets)
- `network_repo.go` con métodos
- `network_reconciler.go` interface + scheduler con tiers
- `nimos_secrets.go` AES-GCM helpers
- Migration scripts JSON → SQLite (limpio + cifrar tokens en migración)
- Helpers: `HealthStatus`, `Convergence`, `SystemCapabilities`, `CircuitBreaker`

### F-002 — Network Observer + Capability Detection

**Coste**: ~5h (era 4h, +1h por capabilities + persistence)
**Outputs**:
- `network_observer.go` con probes pluggables
- Persistencia de snapshots en `network_observed` (retención: 100 + 30 días)
- `SystemCapabilities` detection al boot + refresh
- `HealthStatus` aplicado a CADA observed entity
- Endpoint `/api/network/observed` + `/api/network/observed/history`

### F-003 — Puertos configurables vía UI ⭐ FRICCIÓN INMEDIATA

**Coste**: ~3h
- (Mismo que v2 + uso de triple generation para reload graceful)

### F-004 — DDNS Reconciler (Medium tier) + Circuit Breaker

**Coste**: ~4h (era 3h, +1h por breaker integration)
- BreakerProvider wrapping DuckDNSProvider, NoIPProvider, etc.
- Backoff exponencial al fallar
- DDNS check NO se ejecuta si breaker open

### F-005 — Cert + DNS Providers desacoplados + Breaker ⭐ ARQUITECTURA

**Coste**: ~7h (era 6h, +1h por breaker en providers)
- CertProvider interface (letsencrypt, selfsigned)
- DNSChallengeProvider interface (duckdns, cloudflare stub)
- Cada provider wrapped en CircuitBreaker

### F-006 — Diagnóstico pre-cert con capability awareness

**Coste**: ~3h
- Comprueba `SystemCapabilities` antes de ofrecer cada flujo:
  - "DNS-01 no disponible: necesita certbot + python3-dnspython"
  - "HTTP-01 no disponible: puerto 80 no accesible"
- UI muestra checklist con hints específicos

### F-007 — UPnP best-effort + UX honesta + Breaker

**Coste**: ~5h
- Wrapped en CircuitBreaker (UPnP falla mucho)
- HealthStatus claro: healthy/failed/unknown
- UX: "Tu router no responde a UPnP (común en Movistar/Vodafone)"
- Fallback: instrucciones manuales claras

### F-008 — Polling cleanup + Event categorization

**Coste**: ~2h (era 0.5h, +1.5h por refactor events)
- Eventos categorizados retroactivamente
- Polling lazy + cached
- API endpoints con `Cache-Control: max-age=60`

---

## ORDEN DE IMPLEMENTACIÓN v3

```
SESIÓN 1 (~7h): F-001 Schema + Repo + Reconciler base + AES-GCM
   ⭐ FUNDACIÓN CRÍTICA — sin esto el resto no funciona
   
SESIÓN 2 (~5h): F-002 Observer + Capabilities + Persistence
   ⭐ ESTADO ANTES DE ACCIONES — saber qué hay antes de cambiar
   
SESIÓN 3 (~3h): F-003 Puertos configurables
   Fricción real del caso de Andrés (Synology vs NimOS)
   
SESIÓN 4 (~4h): F-004 DDNS Reconciler + Breaker
   DDNS finalmente funcional con resiliencia
   
SESIÓN 5 (~7h): F-005 Cert + DNS providers + Breaker
   Cert robusto y extensible
   
SESIÓN 6 (~3h): F-006 Diagnóstico pre-cert
   UX brutal mejora
   
SESIÓN 7 (~5h): F-007 UPnP best-effort
   Asistente opcional honesto
   
SESIÓN 8 (~2h): F-008 Polling + categorization
   
SESIÓN 9 (~2h): Tests E2E + documentación
```

**TOTAL: ~38h en 9 sesiones disciplinadas.**

(v2 decía 30h. v3 son 38h por:
- AES-GCM secrets (+2h)
- Capabilities detection (+1h)
- Snapshot persistence (+1h)
- Circuit breakers (+3h dispersos)
- Event categorization (+1h)

Pero TODA esa complejidad es JUSTIFICADA y reutilizable en otros módulos.
Es deuda evitada en Beta 9, 10, 11...)

---

## CRITERIOS DE CIERRE v3

```
✓ Todo en SQLite (NO JSON anywhere)
✓ Tokens AES-GCM at-rest desde día 1
✓ Reconciler pattern con priority tiers
✓ Cert + DNS providers desacoplados, ambos en breaker
✓ NetworkObserver con atomic.Pointer + snapshots persistidos
✓ Triple Generation (Desired/Observed/Applied) en todas las entities
✓ HealthStatus enum unificado en TODOS los observed objects
✓ SystemCapabilities detection + UI muestra qué falta
✓ CircuitBreaker en cada provider externo
✓ Operations con triggered_by + request_id
✓ Events con category + event indexados
✓ UPnP best-effort con UX honesta
✓ Diagnóstico pre-cert estructurado
✓ DDNS auto-update funcional
✓ Puertos configurables sin reiniciar Pi
✓ Build/test/race/vet TODO VERDE
✓ Test E2E real (NAS Raspberry Pi):
   · Cambiar puerto → reload sin caída
   · DDNS detecta cambio de IP y actualiza
   · Breaker abre al 5º fallo DuckDNS, cooldown 5 min
   · Cert DNS-01 emite sin tocar router
   · Self-signed emite en 5 segundos
   · UPnP intenta y reporta razón clara si falla
   · Diagnóstico muestra checks con hints específicos
   · Observer detecta cert expirando, marca degraded
   · Snapshots persistidos consultables por timestamp
   · Capability detection refresca al instalar/desinstalar tools
```

---

## PATRONES REUTILIZABLES EN OTROS MÓDULOS

Estos patrones nacen en Network pero se aplicarán a TODO NimOS:

```
1. HealthStatus enum unificado
   → Storage health, App health, System health, etc.
   
2. Triple Generation (Desired/Observed/Applied)
   → Storage pools, Backups, Apps deployed, etc.
   
3. SystemCapabilities detection
   → Detectar btrfs vs zfs, docker vs podman, etc.
   
4. CircuitBreaker para providers externos
   → Cloud backup, App store, Notifications, etc.
   
5. Snapshot persistido en SQLite
   → Storage observer también debería persistir
   
6. Reconciler con priority tiers
   → Storage reconciler, Backup reconciler, etc.
   
7. Triggered_by + request_id en operations
   → Storage operations también debería tener esto
   
8. Events con category + event indexados
   → Auditoría unificada en todo NimOS
   
9. Secrets table con AES-GCM
   → SSH keys, API keys, passwords de SMB, etc.
```

**Decisión arquitectónica: estos 9 patrones se documentan como
NIMOS_PATTERNS.md y se aplican a TODO módulo nuevo.**

---

## SCOPE FUTURO (Beta 9+)

```
- VPN integration (WireGuard, Tailscale, ZeroTier)
- Cloudflare/Route53 como DNSChallengeProvider funcionales
- ZeroSSL como CertProvider alternativo
- Bandwidth monitoring
- Custom DNS resolver / Pi-hole integration
- Reverse proxy configurable desde UI
- Multi-domain certs (SAN)
- Key rotation automática para nimos_secrets
- IPv6 first-class
- Bonjour/mDNS advertising avanzado
```

---

## CRÉDITOS

```
v2 → v3 mejorado tras segunda ronda de crítica arquitectónica
de Andrés (19/05/2026 noche).

Sus 10 puntos aplicados:

  1. network_observed persistido en SQLite       ✅ network_observed table
  2. Tokens DDNS AES-GCM (no esperar Beta 9)     ✅ nimos_secrets + F-001
  3. Ownership en operaciones                    ✅ triggered_by + request_id
  4. Capability detection formal                 ✅ SystemCapabilities + UI
  5. Circuit breaker para providers externos     ✅ CircuitBreaker pattern
  6. Reconciler priority tiers                   ✅ ReconcilerTier enum
  7. Triple Generation (Desired/Observed/Applied)✅ Convergence struct
  8. Events con category indexada                ✅ category + event columns
  9. HealthStatus enum unificado                 ✅ HealthStatus type global
 10. Observed/Declared/Reconcile como filosofía  ✅ Patrón consistente

Sin esta crítica, Network Module habría salido como un módulo
"funcional pero ad-hoc". Con esta crítica, sale como un módulo
arquitectónicamente sólido y los patrones se aplican a TODO NimOS.

NimOS gana 9 patrones reutilizables. Eso es valor permanente.
```
