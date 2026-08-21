package request

type OidcGoogleRequest struct {
	Iss           string `json:"iss"`
	Sub           string `json:"sub"`
	Aud           string `json:"aud"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Iat           int64  `json:"iat"`
	Exp           int64  `json:"exp"`
}
