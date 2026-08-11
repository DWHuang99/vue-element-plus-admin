//go:build !rollback

// Shipped-build router list for the public compatibility suite (T082): the
// legacy monolith router is decommissioned in this build, so the suite runs
// against the Admin BFF router only. The rollback-window variant
// (compat_builders_legacy_test.go) adds the legacy router under `-tags
// rollback`. The two variants are mutually exclusive; exactly one compiles
// with compat_suite_test.go.
package http

// builders returns the router constructors the compatibility suite must stay
// compatible with. In the shipped build that is the Admin BFF router alone.
func builders() []routerBuilder {
	return []routerBuilder{
		{name: "admin_bff", build: buildBFFRouter},
	}
}
