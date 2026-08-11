// T073: the HTTP request ID and the envelope v1 correlation ID must be the
// same bounded language. The middleware accepts only IDs that can be placed
// into an outbox envelope untouched (research.md Decision 13: "所有公开请求
// 和模块 port 调用传播 request ID/correlation ID"). The two regexes live in
// their boundary packages by design — internal/integration must not import
// the gin-based middleware — so this test pins their equivalence.
package architecture

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hdw/vue-element-plus-admin/backend/internal/integration"
	"github.com/hdw/vue-element-plus-admin/backend/internal/middleware"
)

func TestRequestIDCharsetMatchesEnvelopeCorrelation(t *testing.T) {
	samples := []string{
		// members of the charset
		"my-custom-id-123",
		"a.b:c_d-e",
		"12345",
		"Z",
		"0",
		strings.Repeat("a", 128), // exact max length
		// outside the charset
		"",
		"has space",
		"tab\tid",
		"unicode-汉字",
		"quote\"inject",
		"newline\ninject",
		".leading-dot",
		"$dollar",
		"<angle>",
		strings.Repeat("a", 129), // overlong
	}
	for _, s := range samples {
		assert.Equalf(t, middleware.ValidRequestID(s), integration.ValidCorrelationID(s),
			"request ID charset must equal envelope correlation charset for %q", s)
	}
}
