package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// presignHMAC generates an HMAC-SHA256 signature for a (key, expires) pair.
func presignHMAC(key string, expires int64, signingKey string) string {
	mac := hmac.New(sha256.New, []byte(signingKey))
	fmt.Fprintf(mac, "%s|%d", key, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyPresignedURL validates the HMAC signature and expiration of a presigned
// download URL. It is shared by both the local and S3 backends.
func verifyPresignedURL(rawURL string, now time.Time, signingKey string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	key := q.Get("key")
	expStr := q.Get("exp")
	gotSig := q.Get("sig")
	if key == "" || expStr == "" || gotSig == "" {
		return errors.New("presign: missing params")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return fmt.Errorf("presign: bad exp: %w", err)
	}
	if now.Unix() > exp {
		return errors.New("presign: expired")
	}
	wantSig := presignHMAC(key, exp, signingKey)
	if !hmac.Equal([]byte(gotSig), []byte(wantSig)) {
		return errors.New("presign: bad signature")
	}
	return nil
}
