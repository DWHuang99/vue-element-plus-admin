// Package adminapi is the single-process composition root (plan.md: the only
// place allowed to import every concrete adapter). It owns the legacy vs
// Admin BFF route switch, the per-module readiness probes (T071) and the
// outbox dispatcher lifecycle.
package adminapi

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/adminbff"
	adminbffpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/postgres"
	adminbfhttp "github.com/hdw/vue-element-plus-admin/backend/internal/adminbff/transport/http"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/health"
	"github.com/hdw/vue-element-plus-admin/backend/internal/iam"
	iampostgres "github.com/hdw/vue-element-plus-admin/backend/internal/iam/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/integration"
	"github.com/hdw/vue-element-plus-admin/backend/internal/logging"
	"github.com/hdw/vue-element-plus-admin/backend/internal/organization"
	orgpostgres "github.com/hdw/vue-element-plus-admin/backend/internal/organization/postgres"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
	"github.com/hdw/vue-element-plus-admin/backend/internal/server"
)

// Config carries the route-switch and background-loop flags consumed by the
// composition root. Granular capability flags land with US2 (plan Phase 5.9
// capability matrix); the master switch forces all granular BFF capabilities
// off when disabled.
type Config struct {
	AdminBFFRoutesEnabled bool
	// OutboxDispatcherEnabled mirrors OUTBOX_DISPATCHER_ENABLED: the
	// in-process outbox dispatcher loop runs (gated by the T070 startup
	// gates — never enabled before legacy delete delegation + bridge
	// mode=false).
	OutboxDispatcherEnabled bool
	// ShadowReadsEnabled mirrors ADMIN_BFF_SHADOW_READS ANDed with the
	// master switch: pure read routes replay through the legacy router and
	// compare results (T075). Only ever the side-effect-free RBAC reads —
	// never the auth path (sessions must not slide from shadow traffic).
	ShadowReadsEnabled bool
	// LegacyDeleteIAMDelegationEnabled mirrors LEGACY_DELETE_IAM_DELEGATION_ENABLED:
	// when set, the legacy POST /users/delete route runs through the IAM
	// DeleteUsers port (outbox + receipt) instead of the legacy direct delete
	// (T076). The startup gates require it before any consumer/dispatcher or
	// BFF delete-route acceptance.
	LegacyDeleteIAMDelegationEnabled bool
	// RolloutGate carries the T077 writer-loop values; the writer runs in
	// StartBackground when Enabled.
	RolloutGate RolloutGateRuntime
}

// Wire holds the composition root's concrete dependencies.
type Wire struct {
	db     *database.DB
	logger *slog.Logger
	cfg    Config
	cors   []string
	rate   server.LegacyRateLimitConfig

	iamSvc     *iam.Service
	orgSvc     *organization.Service
	health     *health.Handler
	dispatcher *integration.Dispatcher

	// metrics (T074) is the in-process registry shared by every router; the
	// measured port decorators and the polled DB collectors feed it.
	metrics *observability.Registry

	// rolloutGate carries the T077 writer-loop runtime values.
	rolloutGate RolloutGateRuntime

	// bffWorkflows is the single Admin BFF workflow store shared by the BFF
	// service composition and the T078 cleanup coordinator's watermark source
	// (built once in NewWire so both wireings use the same adapter).
	bffWorkflows adminbff.WorkflowStore

	// cleanup is the T078 cross-owner evidence-cleanup coordinator. It is
	// wired in every state; only an explicitly approved purge deletes.
	cleanup *platform.CleanupCoordinator
}

// NewWire creates the composition root: the IAM/Organization services, the
// per-module readiness probes and (when configured) the outbox dispatcher.
// The three database modules currently share one pool — the same physical
// database per the T069 gate — and plug into separate pools when distinct
// module DSNs arrive (plan Phase 7.1).
func NewWire(db *database.DB, logger *slog.Logger, cfg Config, corsOrigins []string, rate server.LegacyRateLimitConfig) *Wire {
	w := &Wire{db: db, logger: logger, cfg: cfg, cors: corsOrigins, rate: rate, rolloutGate: cfg.RolloutGate}
	// Module-scoped loggers (T072): every service line carries its module and
	// service attributes for cross-module traceability. Concrete adapters are
	// captured so the T074 collectors can assert their additive reads.
	iamStore := iampostgres.NewStore(db.Pool)
	orgStore := orgpostgres.NewStore(db.Pool)
	w.iamSvc = iam.NewService(iamStore, logging.WithModule(logger, "iam", "service"))
	w.orgSvc = organization.NewService(orgStore, logging.WithModule(logger, "organization", "service"))
	outboxDelivery := iampostgres.NewOutboxDelivery(db.Pool)

	// T078: the Admin BFF workflow store and the cross-owner cleanup
	// coordinator are built once here so the legacy, BFF and (future) cleanup
	// HTTP paths all share the same adapters. Each owner purge stays
	// owner-local; the coordinator only orchestrates + records the audit.
	w.bffWorkflows = adminbffpostgres.NewStore(db.Pool)
	w.cleanup = platform.NewCleanupCoordinator(
		[]platform.CleanupOwner{
			{Name: "iam", Port: &iamCleanupOwner{inner: iampostgres.NewEvidenceCleanup(db.Pool)}},
			{Name: "organization", Port: &orgCleanupOwner{inner: orgpostgres.NewEvidenceCleanup(db.Pool)}},
		},
		&workflowWatermarkSource{store: w.bffWorkflows},
		&cleanupAuditStore{pool: db.Pool},
		logging.WithModule(logger, "platform", "evidence_cleanup"))

	w.metrics = observability.NewRegistry()
	registerCollectors(w, iamStore, orgStore, outboxDelivery)

	probes := []health.Probe{
		{Name: "iam_database", Check: poolPing(db)},
		{Name: "organization_database", Check: poolPing(db)},
		{Name: "admin_workflow_store", Check: poolPing(db)},
	}
	if cfg.OutboxDispatcherEnabled {
		// The dispatcher consumes the Organization inbox through the measured
		// decorator so inbox-handler latency/errors count like every other port.
		w.dispatcher = integration.NewDispatcher(
			outboxDelivery,
			&measuredInbox{inner: w.orgSvc, reg: w.metrics},
			logging.WithModule(logger, "integration", "dispatcher"), integration.DefaultDispatcherConfig())
	}
	// outbox_dispatcher is registered in every state so readiness always
	// distinguishes it: disabled → ok (not part of the critical path),
	// enabled → the loop must actually be running.
	probes = append(probes, health.Probe{
		Name: "outbox_dispatcher",
		Check: func(ctx context.Context) error {
			if !cfg.OutboxDispatcherEnabled {
				return nil
			}
			if w.dispatcher == nil {
				return errors.New("outbox dispatcher not constructed")
			}
			return w.dispatcher.Healthy()
		},
	})
	w.health = health.NewHandlerWithProbes(db, w.logger, probes)
	return w
}

// poolPing adapts the shared pool to a health.Pinger for the module probes.
func poolPing(db *database.DB) func(ctx context.Context) error {
	return func(ctx context.Context) error { return db.Ping(ctx) }
}

// registerCollectors wires the T074 polled collectors to their concrete
// adapters. NewStore returns the module port interface, so each collector
// asserts only the additive read method it needs on the dynamic concrete type
// (anonymous interface) — the module port interfaces stay untouched; a failed
// assertion logs and skips rather than panicking the composition root.
func registerCollectors(w *Wire, iamStore iam.Store, orgStore organization.Store, outbox *iampostgres.OutboxDelivery) {
	if c, ok := any(outbox).(interface {
		GetOutboxBacklog(ctx context.Context) (iam.OutboxBacklog, error)
	}); ok {
		w.metrics.RegisterCollector(outboxCollector{store: c})
	} else {
		w.logger.Warn("outbox collector not registered: GetOutboxBacklog missing")
	}
	if c, ok := any(iamStore).(interface {
		GetProvisioningSnapshot(ctx context.Context) (iampostgres.ProvisioningSnapshot, error)
	}); ok {
		w.metrics.RegisterCollector(provisioningCollector{store: c})
	} else {
		w.logger.Warn("provisioning collector not registered: GetProvisioningSnapshot missing")
	}
	if c, ok := any(orgStore).(interface {
		GetInboxMetrics(ctx context.Context) (map[string]int64, error)
	}); ok {
		w.metrics.RegisterCollector(inboxCollector{store: c})
	} else {
		w.logger.Warn("inbox collector not registered: GetInboxMetrics missing")
	}
}

// StartBackground launches the background loops when configured — the outbox
// dispatcher (T061) and the rollout-gate writer (T077), each independent of
// the other. The T070 startup gates have already passed by the time this is
// called; every loop stops when ctx is cancelled (graceful shutdown drains
// in-flight deliveries).
func (w *Wire) StartBackground(ctx context.Context) {
	if w.dispatcher != nil {
		go func() {
			if err := w.dispatcher.Run(ctx); err != nil {
				w.logger.Error("outbox dispatcher stopped", "error", err)
			}
		}()
		w.logger.Info("outbox dispatcher started",
			"poll_interval", integration.DefaultDispatcherConfig().PollInterval.String())
	}

	if w.rolloutGate.Enabled {
		writer := platform.NewRolloutGateWriter(
			&rolloutGateStore{pool: w.db.Pool},
			&rolloutGateSource{pool: w.db.Pool, registry: w.metrics, cfg: w.rolloutGate},
			w.rolloutGate.SampleCadence,
			logging.WithModule(w.logger, "platform", "rollout_gate"))
		go func() {
			if err := writer.Run(ctx); err != nil {
				w.logger.Error("rollout gate writer stopped", "error", err)
			}
		}()
		w.logger.Info("rollout gate writer started",
			"phase", w.rolloutGate.Phase,
			"cadence", w.rolloutGate.SampleCadence.String(),
			"max_gap", w.rolloutGate.MaxGapInterval.String())
	}
}

// adminBFFRouter assembles the Admin BFF router: IAM and Organization
// adapters through their application services, the BFF composition, and the
// legacy users-write delegation (write endpoints keep the legacy wiring
// until US3, plan Phase 5). Health is mounted from the same per-module probe
// handler the legacy router uses.
func (w *Wire) adminBFFRouter() (*gin.Engine, func()) {
	// The workflow store was built once in NewWire so the BFF service and the
	// cleanup coordinator's watermark source share the same adapter.
	bffWorkflows := w.bffWorkflows

	// Measured decorators (T074): every port the router and the BFF service
	// touch is wrapped, so per-port latency/error classes exist without any
	// module code knowing about the registry.
	auth := &measuredAuth{inner: w.iamSvc, reg: w.metrics}
	identity := &measuredIdentity{inner: w.iamSvc, reg: w.metrics}
	roles := &measuredRoles{inner: w.iamSvc, reg: w.metrics}
	managed := &measuredManaged{inner: w.iamSvc, reg: w.metrics}
	iamReceipts := &measuredIAMReceipts{inner: w.iamSvc, reg: w.metrics}
	depts := &measuredDepartments{inner: w.orgSvc, reg: w.metrics}
	membership := &measuredMembership{inner: w.orgSvc, reg: w.metrics}
	orgReceipts := &measuredOrgReceipts{inner: w.orgSvc, reg: w.metrics}
	inbox := &measuredInbox{inner: w.orgSvc, reg: w.metrics}

	bffSvc := adminbff.NewService(adminbff.IAMParticipants{
		Auth:     auth,
		Identity: identity,
		Roles:    roles,
		Managed:  managed,
		Receipts: iamReceipts,
	}, adminbff.OrganizationParticipants{
		Departments: depts,
		Membership:  membership,
		Receipts:    orgReceipts,
		Inbox:       inbox,
	}, bffWorkflows, logging.WithModule(w.logger, "adminbff", "service"))

	if c, ok := any(bffWorkflows).(interface {
		CountWorkflowsByState(ctx context.Context, state adminbff.WorkflowState) (int64, error)
	}); ok {
		w.metrics.RegisterCollector(workflowCollector{store: c})
	} else {
		w.logger.Warn("workflow collector not registered: CountWorkflowsByState missing")
	}

	// T075 shadow reads: when enabled (rollback build only), pure RBAC read
	// routes replay through a legacy router built alongside the BFF router.
	// buildShadowReads is the build-variant hook: the shipped (!rollback) build
	// returns a disabled stub — shadow reads are a rollback-window capability —
	// and the `-tags rollback` build returns the real replay middleware.
	shadowReads, closeShadow := w.buildShadowReads()

	router, closeRouter := adminbfhttp.NewRouter(adminbfhttp.RouterDeps{
		Service:     bffSvc,
		Auth:        auth,
		Identity:    identity,
		Roles:       roles,
		Departments: depts,
		Logger:      w.logger,
		CORSOrigins: w.cors,
		RateLimit: adminbfhttp.RateLimitConfig{
			Enabled:        w.rate.Enabled,
			RegisterIPHour: w.rate.RegisterIPHour,
			LoginIP15Min:   w.rate.LoginIP15Min,
			LoginUser15Min: w.rate.LoginUser15Min,
		},
		Health: adminbfhttp.HealthRoutes{
			Live:  w.health.Live,
			Ready: w.health.Ready,
		},
		Metrics:     w.metrics,
		ShadowReads: shadowReads,
	})
	return router, func() {
		closeRouter()
		closeShadow()
	}
}
