package permission

import "time"

type DepartmentItem struct {
	ID             int64            `json:"id"`
	ParentID       int64            `json:"parentId"`
	DepartmentName string           `json:"departmentName"`
	Status         int              `json:"status"`
	Deleting       bool             `json:"-"`
	CreateTime     time.Time        `json:"createTime"`
	Remark         string           `json:"remark"`
	Children       []DepartmentItem `json:"children,omitempty"`
}

type UserItem struct {
	ID           int64           `json:"id"`
	Username     string          `json:"username"`
	Account      string          `json:"account"`
	Email        string          `json:"email"`
	CreateTime   time.Time       `json:"createTime"`
	Role         string          `json:"role"`
	RoleID       int64           `json:"roleId"`
	Department   *DepartmentItem `json:"department,omitempty"`
	DepartmentID int64           `json:"-"`
}

type PermissionItem struct {
	ID    int64  `json:"id"`
	Value string `json:"value"`
	Label string `json:"label"`
}

type MenuMeta struct {
	Title      string   `json:"title"`
	Icon       string   `json:"icon,omitempty"`
	ActiveMenu string   `json:"activeMenu,omitempty"`
	Hidden     bool     `json:"hidden,omitempty"`
	AlwaysShow bool     `json:"alwaysShow,omitempty"`
	NoCache    bool     `json:"noCache,omitempty"`
	Breadcrumb bool     `json:"breadcrumb,omitempty"`
	Affix      bool     `json:"affix,omitempty"`
	NoTagsView bool     `json:"noTagsView,omitempty"`
	CanTo      bool     `json:"canTo,omitempty"`
	Permission []string `json:"permission,omitempty"`
}

type MenuItem struct {
	ID             int64            `json:"id"`
	ParentID       int64            `json:"parentId"`
	ParentName     string           `json:"parentName,omitempty"`
	Type           int              `json:"type"`
	Path           string           `json:"path"`
	Name           string           `json:"name"`
	Component      string           `json:"component"`
	Status         int              `json:"status"`
	Meta           MenuMeta         `json:"meta"`
	PermissionList []PermissionItem `json:"permissionList"`
	Children       []MenuItem       `json:"children,omitempty"`
}

type RoleItem struct {
	ID         int64      `json:"id"`
	Code       string     `json:"code"`
	RoleName   string     `json:"roleName"`
	Status     int        `json:"status"`
	CreateTime time.Time  `json:"createTime"`
	Remark     string     `json:"remark"`
	Menu       []MenuItem `json:"menu"`
}
