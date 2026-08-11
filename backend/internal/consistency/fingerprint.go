// Canonical participant command fingerprint v1 (consistency-and-compensation.md
// "Canonical participant command fingerprint v1").
//
// Every BFF/participant adapter computes the same fingerprint:
//
//	sha256(RFC8785_JCS({"fingerprint_version":1,"command_name":...,
//	                     "actor_user_id":...,"args":{...}}))
//
// Args rules: validated strings encoded exactly with no later normalization;
// optional fields are explicit JSON null; integers are JSON numbers; ID/role
// ID/delete-target sets are deduplicated and sorted ascending (SortedIDs);
// object keys use RFC 8785 ordering (MarshalJCS). Password/token/hash/
// password-derived material is never part of args — only
// password_change_requested: true|false is included, and the builder rejects
// any args key that smells like credential material so an accidental leak
// becomes a loud error instead of a stored fingerprint.
package consistency

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// FingerprintVersion is the canonical schema version carried in the object.
const FingerprintVersion = 1

// forbiddenArgKey reports whether an args key could carry credential
// material. Only password_change_requested is allowed to mention the word.
func forbiddenArgKey(key string) bool {
	lower := strings.ToLower(key)
	if lower == "password_change_requested" {
		return false
	}
	for _, part := range []string{"password", "token", "hash", "secret", "credential", "passwd"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// FingerprintV1 computes the canonical v1 fingerprint for a participant
// command. args keys are validated for credential material; the serialized
// object is RFC 8785 canonical, so the returned hex is stable across
// adapters and Go versions.
func FingerprintV1(commandName string, actorUserID int64, args map[string]any) (string, error) {
	canonical, err := canonicalV1(commandName, actorUserID, args)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalV1 builds the canonical JSON object without hashing it; exposed
// for tests and for adapters that store the canonical bytes alongside the
// hex fingerprint.
func canonicalV1(commandName string, actorUserID int64, args map[string]any) ([]byte, error) {
	if commandName == "" {
		return nil, fmt.Errorf("consistency: empty command_name")
	}
	if args == nil {
		return nil, fmt.Errorf("consistency: nil args")
	}
	for key := range args {
		if forbiddenArgKey(key) {
			return nil, fmt.Errorf("consistency: credential material must not enter a fingerprint (args key %q)", key)
		}
	}
	object := map[string]any{
		"fingerprint_version": FingerprintVersion,
		"command_name":        commandName,
		"actor_user_id":       actorUserID,
		"args":                args,
	}
	return MarshalJCS(object)
}

// OptInt maps a pointer to an explicit JSON null when absent: optional
// fields must be present-and-null, never missing.
func OptInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// OptString maps a pointer to an explicit JSON null when absent.
func OptString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

// SortedIDs deduplicates and sorts an ID set ascending (contract rule for
// role IDs and delete targets); nil input yields an empty slice so the
// canonical bytes never depend on caller-side slice identity.
func SortedIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	out := append([]int64(nil), ids...)
	slices.Sort(out)
	// dedupe in place
	write := 0
	for _, id := range out {
		if write == 0 || out[write-1] != id {
			out[write] = id
			write++
		}
	}
	return out[:write]
}
