package jwtservice

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const JWKSPath = "/.well-known/jwks.json"

type JWK struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWKSHandler struct {
	document JWKS
}

func NewJWKSHandler(publicKey *rsa.PublicKey, keyID string) *JWKSHandler {
	return &JWKSHandler{
		document: JWKS{Keys: []JWK{{
			KeyType:   "RSA",
			Use:       "sig",
			KeyID:     keyID,
			Algorithm: jwt.SigningMethodRS256.Alg(),
			Modulus:   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
			Exponent:  base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
		}}},
	}
}

func RegisterJWKSRoutes(router gin.IRoutes, handler *JWKSHandler) {
	router.GET(JWKSPath, handler.GetJWKS)
}

func (h *JWKSHandler) GetJWKS(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, h.document)
}

// NewRemoteJWKSKeyfunc creates a long-lived remote JWKS verifier. Call it once
// during resource-service startup and cancel ctx during shutdown.
func NewRemoteJWKSKeyfunc(ctx context.Context, jwksURL string) (jwt.Keyfunc, error) {
	if jwksURL == "" {
		return nil, errors.New("JWKS URL is required")
	}

	remote, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, err
	}
	return remote.KeyfuncCtx(ctx), nil
}
