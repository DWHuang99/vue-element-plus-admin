package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// RequestIDHeader is the HTTP header name for request ID.
	RequestIDHeader = "X-Request-Id"
	// ResponseTimeHeader is the HTTP header name for response time in milliseconds.
	ResponseTimeHeader = "X-Response-Time"
)

// RequestID returns a Gin middleware that injects a request ID into every request.
// If the client provides a request ID via X-Request-Id header, it is preserved.
// Otherwise, a new UUID v4 is generated.
// The request ID is added to both the request context and the response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader(RequestIDHeader)
		if requestID == "" {
			requestID = uuid.New().String()
		}

		c.Set(RequestIDHeader, requestID)
		c.Header(RequestIDHeader, requestID)
		c.Next()
	}
}
