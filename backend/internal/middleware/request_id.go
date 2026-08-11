package middleware

import (
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// RequestIDHeader is the HTTP header name for request ID.
	RequestIDHeader = "X-Request-Id"
	// ResponseTimeHeader is the HTTP header name for response time in milliseconds.
	ResponseTimeHeader = "X-Response-Time"
)

// requestIDRe is the restricted request-ID charset (T073, research.md
// Decision 13): the same bounded language as the envelope v1 correlation
// charset (internal/integration/event.go correlationRe), so any accepted
// request ID can flow through OperationContext into an outbox event
// untouched. Client-supplied values outside this set are discarded and
// regenerated — never echoed into logs, responses or stored rows.
var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidRequestID reports whether id fits the restricted request-ID charset.
func ValidRequestID(id string) bool {
	return requestIDRe.MatchString(id)
}

// RequestID returns a Gin middleware that injects a request ID into every
// request. A client-provided X-Request-Id is preserved only when it fits the
// restricted charset; otherwise (absent or invalid) a fresh UUID v4 is
// generated. The request ID is never derived from the token or any other
// credential material — it is opaque correlation context only. It is added
// to both the request context and the response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader(RequestIDHeader)
		if !ValidRequestID(requestID) {
			requestID = uuid.New().String()
		}

		c.Set(RequestIDHeader, requestID)
		c.Header(RequestIDHeader, requestID)
		c.Next()
	}
}
