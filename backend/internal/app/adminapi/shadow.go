// Shadow reads (T075): the Admin BFF router serves pure read routes normally
// while replaying the identical request through the legacy monolith router
// and comparing the two responses. A mismatch is logged (with the correlation
// request ID) and counted — the T077 rollout gate consumes the counters.
//
// Only side-effect-free RBAC reads are ever shadowed. The auth path is never
// shadowed: a shadow replay of /me or the session checks would slide session
// expiry twice and duplicate every session side effect (plan.md: "只 shadow
// 无副作用读取；不得 shadow 会滑动 session 的认证调用").
//
// The replay target is built without a metrics registry: shadow traffic is
// internal verification, not production traffic, so it must not pollute the
// production HTTP ratio/latency series.
package adminapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
	"github.com/hdw/vue-element-plus-admin/backend/internal/platform/observability"
)

// bodyCapture wraps the response writer so the middleware can read back what
// the BFF handler wrote without buffering the real client stream.
type bodyCapture struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *bodyCapture) Write(b []byte) (int, error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

// shadowReadsMiddleware returns the shadow-read middleware bound to the
// legacy router. It runs after the BFF handler chain, then replays the exact
// request (method, path, query, headers) through the legacy engine and
// compares status + JSON bodies (order-insensitive). Mismatches and replay
// failures are logged and counted; responses to the client are untouched.
func shadowReadsMiddleware(legacy *gin.Engine, logger *slog.Logger, reg *observability.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		captured := &bodyCapture{ResponseWriter: c.Writer}
		c.Writer = captured
		c.Next()

		if reg != nil {
			reg.AddCounter("shadow_reads_total", 1)
		}
		bffStatus := c.Writer.Status()
		bffBody := captured.buf.Bytes()

		shadowReq := httptest.NewRequest(c.Request.Method, c.Request.URL.RequestURI(), nil)
		shadowReq.Header = c.Request.Header.Clone()
		legacyRec := httptest.NewRecorder()
		legacy.ServeHTTP(legacyRec, shadowReq)

		equal := legacyRec.Code == bffStatus && jsonEqual(legacyRec.Body.Bytes(), bffBody)
		if equal {
			return
		}
		if reg != nil {
			reg.AddCounter("shadow_reads_mismatches_total", 1)
		}
		logger.Warn("shadow read mismatch",
			"request_id", c.GetString(middleware.RequestIDHeader),
			"path", c.Request.URL.Path,
			"bff_status", bffStatus,
			"legacy_status", legacyRec.Code,
			"bodies_equal", jsonEqual(legacyRec.Body.Bytes(), bffBody),
		)
	}
}

// jsonEqual compares two response bodies as JSON documents — key order,
// whitespace and numeric representation differences are ignored. Bodies that
// fail to parse on either side compare by raw bytes instead (status equality
// still caught the divergence above).
func jsonEqual(a, b []byte) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return bytes.Equal(a, b)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(av, bv)
}
