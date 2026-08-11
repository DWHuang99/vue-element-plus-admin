// CLI tests for admin-init.
//
// Grant semantics (registered-user requirement, only admin/super_admin codes,
// idempotence, version bump only on change, preservation of other roles) are
// pinned by the IAM contract suite (TestGrantBuiltInAdminRole_AdminBootstrap
// in internal/iam/service_test.go) against a real PostgreSQL; the command is
// now a thin wrapper over iam.GrantBuiltInAdminRole, so this file only covers
// flag parsing and the CLI wiring that stays in package main.
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOptionsAcceptsAdministrativeRoles(t *testing.T) {
	for _, role := range []string{"admin", "super_admin"} {
		var output bytes.Buffer
		opts, err := parseOptions([]string{"--username", "alice", "--role", role}, &output)
		require.NoError(t, err)
		assert.Equal(t, options{username: "alice", role: role}, opts)
		assert.Empty(t, output.String())
	}
}

func TestParseOptionsRejectsPasswordFlag(t *testing.T) {
	var output bytes.Buffer
	_, err := parseOptions([]string{"--username", "alice", "--role", "admin", "--password", "secret"}, &output)
	require.Error(t, err)
	assert.Contains(t, output.String(), "flag provided but not defined: -password")
}

func TestParseOptionsRejectsInvalidRoleAndMissingUsername(t *testing.T) {
	var output bytes.Buffer
	_, err := parseOptions([]string{"--role", "user"}, &output)
	require.Error(t, err)
	assert.Contains(t, output.String(), "--username is required")

	output.Reset()
	_, err = parseOptions([]string{"--username", "alice", "--role", "operator"}, &output)
	require.Error(t, err)
	assert.Contains(t, output.String(), "--role must be admin or super_admin")
}

func TestRunRequiresDatabaseURLBeforeConnecting(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"--username", "alice", "--role", "admin"}, func(string) string {
		return ""
	}, &stdout, &stderr)

	assert.Equal(t, 1, exitCode)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "DATABASE_URL is required")
}

func TestRunRejectsInvalidFlagsWithUsageExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"--role", "user"}, func(string) string {
		return "postgres://user:pass@localhost/db"
	}, &stdout, &stderr)

	assert.Equal(t, 2, exitCode)
	assert.Empty(t, stdout.String())
}
