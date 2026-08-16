package jwtservice

import (
	"errors"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid token")

type Claims struct {
	Role        []string `json:"role"`
	Permissions []string `json:"permissions,omitempty"`
	// 包含 sub、exp、iat、iss 等标准字段
	jwt.RegisteredClaims
}
