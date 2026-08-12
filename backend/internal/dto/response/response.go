package response

import "time"

type UserInfo struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	RoleID      int64     `json:"roleId"`
	RoleCode    string    `json:"roleCode"`
	RoleName    string    `json:"roleName"`
	Permissions []string  `json:"permissions"`
	IsActive    bool      `json:"isActive"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
