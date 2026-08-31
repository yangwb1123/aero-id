package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"math/big"
	"strings"
)

func verifyJWTSignature(alg string, public crypto.PublicKey, signed, signature []byte) error {
	if alg == "EdDSA" {
		return verifyEdDSASignature(public, signed, signature)
	}
	hashFunc, hashSize, err := jwtHash(alg)
	if err != nil {
		return err
	}
	digest := hashFunc(signed)
	switch public.(type) {
	case *rsa.PublicKey:
		return verifyRSASignature(alg, public, hashSize, digest, signature)
	case *ecdsa.PublicKey:
		return verifyECDSASignature(public, digest, signature)
	default:
		return errors.New("unsupported public key")
	}
}

func verifyEdDSASignature(public crypto.PublicKey, signed, signature []byte) error {
	key, ok := public.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(key, signed, signature) {
		return errors.New("invalid ed25519 signature")
	}
	return nil
}

func verifyRSASignature(alg string, public crypto.PublicKey, hash crypto.Hash, digest, signature []byte) error {
	key, ok := public.(*rsa.PublicKey)
	if !ok {
		return errors.New("invalid rsa public key")
	}
	if strings.HasPrefix(alg, "PS") {
		return rsa.VerifyPSS(key, hash, digest, signature, &rsa.PSSOptions{SaltLength: hash.Size(), Hash: hash})
	}
	return rsa.VerifyPKCS1v15(key, hash, digest, signature)
}

func verifyECDSASignature(public crypto.PublicKey, digest, signature []byte) error {
	key, ok := public.(*ecdsa.PublicKey)
	if !ok || len(signature)%2 != 0 {
		return errors.New("invalid ecdsa signature")
	}
	half := len(signature) / 2
	r := new(big.Int).SetBytes(signature[:half])
	s := new(big.Int).SetBytes(signature[half:])
	if !ecdsa.Verify(key, digest, r, s) {
		return errors.New("invalid ecdsa signature")
	}
	return nil
}

func jwtHash(alg string) (func([]byte) []byte, crypto.Hash, error) {
	switch alg {
	case "RS256", "PS256", "ES256":
		return func(value []byte) []byte { sum := sha256.Sum256(value); return sum[:] }, crypto.SHA256, nil
	case "RS384", "PS384", "ES384":
		return func(value []byte) []byte { sum := sha512.Sum384(value); return sum[:] }, crypto.SHA384, nil
	case "RS512", "PS512", "ES512":
		return func(value []byte) []byte { sum := sha512.Sum512(value); return sum[:] }, crypto.SHA512, nil
	default:
		return nil, 0, errors.New("unsupported jwt hash")
	}
}
