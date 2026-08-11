//go:build !rollback

// Router decommission (T082): the shipped build serves the Admin BFF router
// only. The legacy monolith router and the AdminBFFRoutesEnabled master switch
// are rollback-window wiring — they live behind `//go:build rollback` in
// wire_legacy.go. This file is the `!rollback` variant; the two variants are
// mutually exclusive, so exactly one compiles with the shared wire.go.
package adminapi

import "github.com/gin-gonic/gin"

// Router returns the Admin BFF router and a close func that stops the rate
// limiter goroutines. The legacy fallback (pre-cutover) is decommissioned in
// this build: the shipped binary physically cannot serve legacy routes, and
// the AdminBFFRoutesEnabled master switch is inert here. The composition
// root's per-module readiness handler and metrics surface are mounted on the
// BFF router.
func (w *Wire) Router() (*gin.Engine, func()) {
	return w.adminBFFRouter()
}

// buildShadowReads returns the shadow-read hook adminBFFRouter wires in. In
// the shipped build shadow reads are disabled: they are a rollback-window
// capability, so ShadowReadsEnabled config is inert here. The `-tags rollback`
// variant (wire_legacy.go) builds the real legacy-router replay middleware.
func (w *Wire) buildShadowReads() (gin.HandlerFunc, func()) {
	return nil, func() {}
}
