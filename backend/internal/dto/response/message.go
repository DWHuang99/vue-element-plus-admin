package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Response struct {
	Code    int    `json:"code"`
	Data    any    `json:"data"`
	Message string `json:"message"`
}

func Success(c *gin.Context, data any) {
	SuccessWithStatus(c, http.StatusOK, data, "success")
}

func SuccessWithStatus(c *gin.Context, status int, data any, message string) {
	c.JSON(status, Response{
		Code:    0,
		Data:    data,
		Message: message,
	})
}

func Error(c *gin.Context, status int, code int, message string) {
	c.JSON(status, Response{
		Code:    code,
		Data:    nil,
		Message: message,
	})
}
