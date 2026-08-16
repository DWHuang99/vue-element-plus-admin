package permission

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"vue-element-plus-admin/backend/internal/dto/response"

	"github.com/gin-gonic/gin"
)

func StatusValue(value int) bool {
	return value != 0
}

func BoolStatus(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ParseID(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}

func ParsePage(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("pageIndex", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	return page, size
}

func BindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		response.Error(c, http.StatusBadRequest, 10001, "invalid request")
		return false
	}
	return true
}

func WriteError(c *gin.Context, status int, message string) {
	code := 10500
	if status == http.StatusNotFound {
		code = 40401
	}
	if status == http.StatusConflict {
		code = 40901
	}
	response.Error(c, status, code, message)
}

func Placeholders(count int, start int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(items, ",")
}

func ParentID(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}

func ParseIDs(c *gin.Context) ([]int64, bool) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if !BindJSON(c, &body) {
		return nil, false
	}
	if len(body.IDs) == 0 {
		WriteError(c, http.StatusBadRequest, "ids are required")
		return nil, false
	}
	return body.IDs, true
}
