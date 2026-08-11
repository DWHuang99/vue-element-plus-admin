// T070 startup-gate tests (plan Phase 5.9 step 4, quickstart §4): the
// delete-path flag combinations and the live bridge-mode state are validated
// before consumer/dispatcher/route acceptance. Errors reference flag names
// and reason codes only — never values.
package main

import (
	"testing"

	"github.com/hdw/vue-element-plus-admin/backend/internal/config"
	"github.com/stretchr/testify/require"
)

// baseCfg returns a valid config with every US5 toggle off (the default).
func baseCfg() *config.Config {
	return &config.Config{Features: config.FeaturesConfig{}}
}

// withFlags applies the given toggles to a base config.
func withFlags(t *testing.T, apply func(f *config.FeaturesConfig, b *config.AdminBFFConfig)) *config.Config {
	t.Helper()
	cfg := baseCfg()
	apply(&cfg.Features, &cfg.AdminBFF)
	return cfg
}

var modeLegacySyncOn = &bridgeModeSnapshot{LegacyDeleteSyncEnabled: true, Version: 1}
var modeLegacySyncOff = &bridgeModeSnapshot{LegacyDeleteSyncEnabled: false, Version: 2}

func TestCheckStartupGates_AllOffPasses(t *testing.T) {
	// Default configuration: no delete-path component, no DB read needed.
	require.NoError(t, checkStartupGates(baseCfg(), nil))
}

func TestCheckStartupGates_DelegationAlonePassesInPhase1(t *testing.T) {
	// Plan step 4: delegation opens first while bridge mode may still be true.
	cfg := withFlags(t, func(f *config.FeaturesConfig, _ *config.AdminBFFConfig) {
		f.LegacyDeleteIAMDelegationEnabled = true
	})
	require.NoError(t, checkStartupGates(cfg, modeLegacySyncOn))
	require.NoError(t, checkStartupGates(cfg, modeLegacySyncOff))
}

func TestCheckStartupGates_ConsumerWithoutDelegationRejected(t *testing.T) {
	cfg := withFlags(t, func(f *config.FeaturesConfig, _ *config.AdminBFFConfig) {
		f.IAMDeleteEventConsumerEnabled = true
	})
	err := checkStartupGates(cfg, modeLegacySyncOff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "LEGACY_DELETE_IAM_DELEGATION_ENABLED")
	require.Contains(t, err.Error(), "IAM_DELETE_EVENT_CONSUMER_ENABLED")
}

func TestCheckStartupGates_DispatcherWithoutDelegationRejected(t *testing.T) {
	cfg := withFlags(t, func(f *config.FeaturesConfig, _ *config.AdminBFFConfig) {
		f.OutboxDispatcherEnabled = true
	})
	err := checkStartupGates(cfg, modeLegacySyncOff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "LEGACY_DELETE_IAM_DELEGATION_ENABLED")
	require.Contains(t, err.Error(), "OUTBOX_DISPATCHER_ENABLED")
}

func TestCheckStartupGates_ConsumerDispatcherNeedBridgeModeOff(t *testing.T) {
	base := func() *config.Config {
		return withFlags(t, func(f *config.FeaturesConfig, _ *config.AdminBFFConfig) {
			f.LegacyDeleteIAMDelegationEnabled = true
			f.IAMDeleteEventConsumerEnabled = true
			f.OutboxDispatcherEnabled = true
		})
	}

	// Bridge mode still true → rejected.
	err := checkStartupGates(base(), modeLegacySyncOn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy_delete_sync_enabled=false")
	require.Contains(t, err.Error(), "bridge mode")

	// Bridge mode false → accepted.
	require.NoError(t, checkStartupGates(base(), modeLegacySyncOff))

	// Bridge row unreadable (schema/row missing) → rejected.
	err = checkStartupGates(base(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bridge mode row")
}

func TestCheckStartupGates_DeleteRouteRequiresFullChain(t *testing.T) {
	// Route alone → rejected (needs delegation).
	cfg := withFlags(t, func(_ *config.FeaturesConfig, b *config.AdminBFFConfig) {
		b.UserDeleteRouteEnabled = true
	})
	err := checkStartupGates(cfg, modeLegacySyncOff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ADMIN_BFF_USER_DELETE_ROUTE_ENABLED")
	require.Contains(t, err.Error(), "LEGACY_DELETE_IAM_DELEGATION_ENABLED")

	// Route + delegation but consumer/dispatcher not ready → rejected.
	cfg = withFlags(t, func(f *config.FeaturesConfig, b *config.AdminBFFConfig) {
		f.LegacyDeleteIAMDelegationEnabled = true
		b.UserDeleteRouteEnabled = true
	})
	err = checkStartupGates(cfg, modeLegacySyncOff)
	require.Error(t, err)
	require.Contains(t, err.Error(), "consumer/dispatcher must be ready")

	// Full chain but bridge mode still true → rejected.
	cfg = withFlags(t, func(f *config.FeaturesConfig, b *config.AdminBFFConfig) {
		f.LegacyDeleteIAMDelegationEnabled = true
		f.IAMDeleteEventConsumerEnabled = true
		f.OutboxDispatcherEnabled = true
		b.UserDeleteRouteEnabled = true
	})
	err = checkStartupGates(cfg, modeLegacySyncOn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "legacy_delete_sync_enabled=false")

	// Full chain, bridge mode false, row present → accepted.
	require.NoError(t, checkStartupGates(cfg, modeLegacySyncOff))

	// Full chain but bridge row unreadable → rejected.
	err = checkStartupGates(cfg, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bridge mode row")
}

func TestCheckStartupGates_ErrorsNeverLeakValues(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		mode *bridgeModeSnapshot
	}{
		{"consumer-no-delegation", withFlags(t, func(f *config.FeaturesConfig, _ *config.AdminBFFConfig) {
			f.IAMDeleteEventConsumerEnabled = true
		}), modeLegacySyncOff},
		{"bridge-mode-on", withFlags(t, func(f *config.FeaturesConfig, _ *config.AdminBFFConfig) {
			f.LegacyDeleteIAMDelegationEnabled = true
			f.OutboxDispatcherEnabled = true
		}), modeLegacySyncOn},
		{"route-chain", withFlags(t, func(f *config.FeaturesConfig, b *config.AdminBFFConfig) {
			f.LegacyDeleteIAMDelegationEnabled = true
			f.IAMDeleteEventConsumerEnabled = true
			f.OutboxDispatcherEnabled = true
			b.UserDeleteRouteEnabled = true
		}), modeLegacySyncOn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStartupGates(tc.cfg, tc.mode)
			require.Error(t, err)
			// Gate errors are static requirement phrases (flag names and
			// reason codes only) — they must never carry URL/credential
			// material or dynamically echo the configured boolean state.
			require.NotContains(t, err.Error(), "postgres://")
			require.NotContains(t, err.Error(), "password")
			require.NotContains(t, err.Error(), "currently")
			require.NotContains(t, err.Error(), "got ")
		})
	}
}
