package consistency

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFingerprintV1_PinnedVector pins the canonical object to a fixed sha256
// hex. The object below was hand-verified against the contract
// (consistency-and-compensation.md "Canonical participant command fingerprint
// v1"): fingerprint_version=1, actor_user_id before args before command_name
// before fingerprint_version (RFC 8785 key order), args keys likewise sorted,
// role_ids ascending, password material only as the boolean.
func TestFingerprintV1_PinnedVector(t *testing.T) {
	got, err := FingerprintV1("iam.user.create", 42, map[string]any{
		// deliberately shuffled insertion order; the caller-side contract
		// deduplicates and sorts ID sets via SortedIDs before building args:
		"role_ids":                  SortedIDs([]int64{2, 1, 1, 2}),
		"user_id":                   int64(7),
		"password_change_requested": true,
	})
	require.NoError(t, err)
	assert.Equal(t, "44338a018c11f79de85fe58578a62695666ace0b694e3c8c5bd557ccd5b803ed", got)
}

// TestFingerprintV1_StableAcrossAdapters: the canonical bytes must not depend
// on Go map iteration order, caller-side slice order, or id type widening.
func TestFingerprintV1_StableAcrossAdapters(t *testing.T) {
	a, err := FingerprintV1("org.department.set", 1, map[string]any{
		"user_id":       int64(9),
		"department_id": int64(4),
	})
	require.NoError(t, err)

	// same logical content, different insertion order and int sizes
	b, err := FingerprintV1("org.department.set", 1, map[string]any{
		"department_id": int64(4),
		"user_id":       9, // untyped constant -> int
	})
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

// TestFingerprintV1_OptionalFieldsExplicitNull: absent optional args serialize
// as present-and-null, never missing.
func TestFingerprintV1_OptionalFieldsExplicitNull(t *testing.T) {
	got, err := FingerprintV1("org.department.set", 5, map[string]any{
		"user_id":                     int64(1),
		"department_id":               int64(2),
		"expected_membership_version": OptInt(nil),
	})
	require.NoError(t, err)

	withValue, err := FingerprintV1("org.department.set", 5, map[string]any{
		"user_id":                     int64(1),
		"department_id":               int64(2),
		"expected_membership_version": OptInt(int64Ptr(3)),
	})
	require.NoError(t, err)
	assert.NotEqual(t, got, withValue, "explicit null and a value must differ")

	// and the null is present in the canonical bytes
	canonical, err := canonicalV1("org.department.set", 5, map[string]any{
		"user_id":                     int64(1),
		"department_id":               int64(2),
		"expected_membership_version": OptInt(nil),
	})
	require.NoError(t, err)
	assert.Contains(t, string(canonical), `"expected_membership_version":null`)
}

// TestFingerprintV1_RejectsCredentialMaterial: any args key that could carry
// password/token/hash/secret/credential material is refused loudly; the
// password_change_requested boolean is the only allowed exception.
func TestFingerprintV1_RejectsCredentialMaterial(t *testing.T) {
	for _, key := range []string{
		"password", "password_hash", "new_password", "old_password",
		"reset_token", "access_token", "token", "refresh_token",
		"secret", "client_secret", "credential", "credentials",
		"password_change_request", // not the allowed boolean
	} {
		_, err := FingerprintV1("iam.user.update", 1, map[string]any{key: "x"})
		assert.Error(t, err, "key %q must be rejected", key)
	}

	got, err := FingerprintV1("iam.user.create", 1, map[string]any{
		"password_change_requested": false,
		"user_id":                   int64(1),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, got, "password_change_requested is the allowed boolean")
}

// TestFingerprintV1_ValidationErrors: empty command name and nil args have no
// canonical form.
func TestFingerprintV1_ValidationErrors(t *testing.T) {
	_, err := FingerprintV1("", 1, map[string]any{})
	assert.Error(t, err, "empty command_name")

	_, err = FingerprintV1("iam.user.create", 1, nil)
	assert.Error(t, err, "nil args")
}

// TestSortedIDs_DedupesAndSorts: ID sets are deduplicated and ascending; nil
// yields an empty slice so canonical bytes never depend on caller identity.
func TestSortedIDs_DedupesAndSorts(t *testing.T) {
	assert.Equal(t, []int64{1, 2, 3}, SortedIDs([]int64{3, 1, 3, 2, 1}))
	assert.Equal(t, []int64{}, SortedIDs(nil))
	assert.Equal(t, []int64{}, SortedIDs([]int64{}))
}

func int64Ptr(v int64) *int64 { return &v }
