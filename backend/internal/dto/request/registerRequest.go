package request

type RegisterRequest struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	CheckPassword string `json:"check_password"`
	Code          string `json:"code"`
	IAgree        bool   `json:"iAgree"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
