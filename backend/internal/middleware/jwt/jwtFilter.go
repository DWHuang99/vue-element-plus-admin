package jwtservice

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func JwtFilter(jwtManager *JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorization := c.GetHeader("Authorization")

		parts := strings.Fields(authorization)

		if len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(
				http.StatusUnauthorized,
				gin.H{
					"error": "missing or invalid authorization header",
				},
			)
			return
		}

		tokenString := parts[1]

		claims, err := jwtManager.ParseToken(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(
				http.StatusUnauthorized,
				gin.H{
					"error": "invalid or expired token",
				},
			)
			return
		}

		// 将解析出的用户信息放进本次请求的 Context
		c.Set("userID", claims.Subject)
		c.Set("role", claims.Role)

		// 继续执行后面的中间件和 Handler
		c.Next()
	}
}
