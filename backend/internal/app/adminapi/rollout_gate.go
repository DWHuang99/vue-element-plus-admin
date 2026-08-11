// Rollout-gate composition-root adapter (T077): the platform writer is
// deliberately thin (internal/platform/rollout_gate.go); assembling one
// cadence observation requires touching every module — the bridge-mode row,
// the legacy/users vs new/organization-state parity counts and canonical
// checksums, the shadow-reads mismatch counter from the in-process registry,
// and the rollout identity pinned at composition time — so the sample source
// and the store adapter live here, the only package allowed to import every
// concrete adapter. The window math itself stays in the migration-000012
// SECURITY DEFINER function behind the store port.
package adminapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hdw/vue-element-plus-admin/backend/internal/database"
	"github.com/hdw/vue-element-plus-admin/backend/internal/database/sqlc"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform"
)

// RolloutGateRuntime carries the writer-loop values assembled by main.go
// from the parsed config (T077). CapabilityManifestHash is computed over
// the deployed capability set so gate samples provably correspond to the
// deployed manifest.
type RolloutGateRuntime struct {
	Enabled                bool
	SampleCadence          time.Duration
	MaxGapInterval         time.Duration
	Phase                  string
	PrincipalID            string
	RollbackArtifactID     string
	RollbackSuiteResult    string
	CapabilityManifestHash string
}

// CapabilityManifestHash fingerprints a capability set: sha256 over sorted
// "FLAG=value" lines, so identical sets always hash identically regardless
// of map order. main.go builds the map from the parsed config flags.
func CapabilityManifestHash(flags map[string]bool) string {
	names := make([]string, 0, len(flags))
	for name := range flags {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		if flags[name] {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// rolloutGateStore adapts the database wrapper (which calls the SECURITY
// DEFINER function) to the platform port.
type rolloutGateStore struct {
	pool *pgxpool.Pool
}

func (s *rolloutGateStore) RecordRolloutGateSample(ctx context.Context, sample platform.RolloutGateSample) (int64, error) {
	return database.RecordRolloutGateSample(ctx, s.pool, database.RolloutGateSample{
		Phase:                    sample.Phase,
		CapabilityManifestHash:   sample.CapabilityManifestHash,
		BridgeModeVersion:        sample.BridgeModeVersion,
		BridgeLegacyDeleteSyncOn: sample.BridgeDeleteSyncOn,
		LegacyRows:               sample.LegacyRows,
		NewRows:                  sample.NewRows,
		RowVersionChecksum:       sample.RowVersionChecksum,
		MismatchCount:            sample.MismatchCount,
		LastMismatchAt:           sample.LastMismatchAt,
		RollbackArtifactID:       sample.RollbackArtifactID,
		RollbackSuiteResult:      sample.RollbackSuiteResult,
		PrincipalID:              sample.PrincipalID,
		ObservedAt:               sample.ObservedAt,
		MaxGapInterval:           sample.MaxGapInterval,
	})
}

// metricReader is the registry surface the source needs (observability
// Registry satisfies it).
type metricReader interface {
	CounterValue(name string) int64
}

// rolloutGateSource assembles one cadence observation from live state.
// Parity: legacy/users vs new/organization-state canonical serialization
// (id, version, department) — count equality AND checksum equality. A
// parity violation surfaces as a mismatch (counter increment + fresh
// last-mismatch stamp), so the SQL function resets the window; persistent
// violations keep re-resetting it.
type rolloutGateSource struct {
	pool     *pgxpool.Pool
	registry metricReader
	cfg      RolloutGateRuntime
}

func (s *rolloutGateSource) NextRolloutGateSample(ctx context.Context) (platform.RolloutGateSample, error) {
	mode, err := sqlc.New(s.pool).GetCompatibilityBridgeMode(ctx)
	if err != nil {
		return platform.RolloutGateSample{}, fmt.Errorf("read bridge mode: %w", err)
	}

	// Canonical serialization of both representations; md5 of a NULL
	// string_agg is NULL, so coalesce to '' for empty tables.
	var legacyRows, newRows int64
	var legacyMD5, newMD5 string
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(md5(string_agg(concat(id, ':', version, ':', coalesce(department_id::text, 'null')), ',' ORDER BY id)), '')
		FROM users`).Scan(&legacyRows, &legacyMD5)
	if err != nil {
		return platform.RolloutGateSample{}, fmt.Errorf("legacy parity: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(md5(string_agg(concat(user_id, ':', membership_version, ':', coalesce(department_id::text, 'null')), ',' ORDER BY user_id)), '')
		FROM organization_user_departments`).Scan(&newRows, &newMD5)
	if err != nil {
		return platform.RolloutGateSample{}, fmt.Errorf("new parity: %w", err)
	}

	parityOK := legacyRows == newRows && legacyMD5 == newMD5

	// Cumulative mismatch surface: shadow-reads divergences (0 when shadow
	// is disabled — the counter is only created on the first mismatch) plus
	// parity violations detected here. The SQL function compares the
	// cumulative count against the previous sample, so only increases fire.
	shadowMismatches := s.registry.CounterValue("shadow_reads_mismatches_total")
	mismatchCount := shadowMismatches
	var lastMismatchAt *time.Time
	if !parityOK {
		mismatchCount++
		now := time.Now()
		lastMismatchAt = &now
	}

	now := time.Now()
	joint := sha256.Sum256([]byte(legacyMD5 + newMD5))
	return platform.RolloutGateSample{
		Phase:                  s.cfg.Phase,
		CapabilityManifestHash: s.cfg.CapabilityManifestHash,
		BridgeModeVersion:      mode.Version,
		BridgeDeleteSyncOn:     mode.LegacyDeleteSyncEnabled,
		LegacyRows:             legacyRows,
		NewRows:                newRows,
		RowVersionChecksum:     hex.EncodeToString(joint[:]),
		MismatchCount:          mismatchCount,
		LastMismatchAt:         lastMismatchAt,
		RollbackArtifactID:     s.cfg.RollbackArtifactID,
		RollbackSuiteResult:    s.cfg.RollbackSuiteResult,
		PrincipalID:            s.cfg.PrincipalID,
		ObservedAt:             now,
		MaxGapInterval:         s.cfg.MaxGapInterval,
	}, nil
}
