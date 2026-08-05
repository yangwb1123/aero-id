package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
)

type jwkKey struct {
	alg    string
	public crypto.PublicKey
}

type jwkDocument struct {
	Keys []jwkValue `json:"keys"`
}

type jwkValue struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func decodeJWKS(data []byte) (map[string]jwkKey, error) {
	var document jwkDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	result := make(map[string]jwkKey, len(document.Keys))
	for _, value := range document.Keys {
		if value.Kid == "" || value.Use == "enc" {
			continue
		}
		key, err := parseJWK(value)
		if err != nil {
			continue
		}
		result[value.Kid] = key
	}
	if len(result) == 0 {
		return nil, errors.New("jwks contains no usable signing keys")
	}
	return result, nil
}

func parseJWK(value jwkValue) (jwkKey, error) {
	switch value.Kty {
	case "OKP":
		return parseOKPKey(value)
	case "RSA":
		return parseRSAKey(value)
	case "EC":
		return parseECKey(value)
	default:
		return jwkKey{}, errors.New("unsupported jwk type")
	}
}

func parseOKPKey(value jwkValue) (jwkKey, error) {
	if value.Crv != "Ed25519" {
		return jwkKey{}, errors.New("unsupported okp curve")
	}
	x, err := base64.RawURLEncoding.DecodeString(value.X)
	if err != nil || len(x) != ed25519.PublicKeySize {
		return jwkKey{}, errors.New("invalid ed25519 key")
	}
	return jwkKey{alg: value.Alg, public: ed25519.PublicKey(x)}, nil
}

func parseRSAKey(value jwkValue) (jwkKey, error) {
	n, err := decodeBig(value.N)
	if err != nil {
		return jwkKey{}, err
	}
	e, err := decodeBig(value.E)
	if err != nil || !e.IsInt64() || e.Int64() < 2 {
		return jwkKey{}, errors.New("invalid rsa exponent")
	}
	return jwkKey{alg: value.Alg, public: &rsa.PublicKey{N: n, E: int(e.Int64())}}, nil
}

func parseECKey(value jwkValue) (jwkKey, error) {
	curve, ok := namedCurve(value.Crv)
	if !ok {
		return jwkKey{}, errors.New("unsupported ec curve")
	}
	x, err := decodeBig(value.X)
	if err != nil {
		return jwkKey{}, err
	}
	y, err := decodeBig(value.Y)
	if err != nil || !curve.IsOnCurve(x, y) {
		return jwkKey{}, errors.New("invalid ec point")
	}
	return jwkKey{alg: value.Alg, public: &ecdsa.PublicKey{Curve: curve, X: x, Y: y}}, nil
}

func decodeBig(value string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("invalid jwk integer")
	}
	return new(big.Int).SetBytes(raw), nil
}

func namedCurve(name string) (elliptic.Curve, bool) {
	switch name {
	case "P-256":
		return elliptic.P256(), true
	case "P-384":
		return elliptic.P384(), true
	case "P-521":
		return elliptic.P521(), true
	default:
		return nil, false
	}
}
