package jwtservice

import (
	"net/http"
	"strings"
	"vue-element-plus-admin/backend/internal/dto/response"

	"github.com/gin-gonic/gin"
)

func JwtFilter(jwtManager *JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorization := c.GetHeader("Authorization")

		parts := strings.Fields(authorization)

		if len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") {
			response.Error(c, http.StatusUnauthorized, 401, "missing or invalid authorization header")
			c.Abort()
			return
		}

		tokenString := parts[1]

		claims, err := jwtManager.ParseToken(tokenString)
		if err != nil {
			response.Error(c, http.StatusUnauthorized, 401, "invalid or expired token")
			c.Abort()
			return
		}

		// 将解析出的用户信息放进本次请求的 Context
		c.Set("username", claims.Subject)
		c.Set("role", claims.Role)

		// 继续执行后面的中间件和 Handler
		c.Next()
	}
}
