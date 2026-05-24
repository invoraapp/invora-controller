// Package controller — RS256 JWT bearer source for Zitadel System API auth.
//
// Mirrors the shape in shared/devops/zitadel-controller/internal/zitadelclient/
// system_api.go so the billing-controller event subscriber can authenticate
// against Zitadel without taking on the full zitadelclient package.
package controller

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	zoidc "github.com/zitadel/oidc/v3/pkg/oidc"
	"golang.org/x/oauth2"
)

const systemAPITokenTTL = 1 * time.Hour

type systemAPITokenSource struct {
	signer  jose.Signer
	issuer  string
	aud     string
	expTime time.Duration
}

// newSystemAPITokenSource returns a self-signed RS256 JWT token source bound
// to a Zitadel System API user. Tokens are reused via oauth2.ReuseTokenSource
// so each Zitadel call doesn't re-sign.
func newSystemAPITokenSource(privateKeyPEM []byte, user, audience string) (oauth2.TokenSource, error) {
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing RSA private key: %w", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		&jose.SignerOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("creating signer: %w", err)
	}
	if audience == "" {
		audience = user
	}
	ts := &systemAPITokenSource{
		signer:  signer,
		issuer:  user,
		aud:     audience,
		expTime: systemAPITokenTTL,
	}
	return oauth2.ReuseTokenSource(nil, ts), nil
}

func (s *systemAPITokenSource) Token() (*oauth2.Token, error) {
	now := time.Now()
	claims := jwt.Claims{
		Issuer:   s.issuer,
		Subject:  s.issuer,
		Audience: jwt.Audience{s.aud},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(now.Add(s.expTime)),
	}
	raw, err := jwt.Signed(s.signer).Claims(claims).Serialize()
	if err != nil {
		return nil, fmt.Errorf("signing JWT: %w", err)
	}
	return &oauth2.Token{
		AccessToken: raw,
		TokenType:   string(zoidc.BearerToken),
		Expiry:      now.Add(s.expTime),
	}, nil
}

func parseRSAPrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}
