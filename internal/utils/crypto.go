// Package utils groups the small helpers shared by the services: secure
// random generation, hashing, token creation and CIDR matching.
package utils

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RandomHex returns n random bytes encoded as a hex string (2n characters).
func RandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// MustRandomHex panics when the entropy source is unavailable (boot time only).
func MustRandomHex(n int) string {
	value, err := RandomHex(n)
	if err != nil {
		panic(err)
	}
	return value
}

// NewUUID returns a RFC 4122 version 4 UUID.
func NewUUID() string {
	return uuid.NewString()
}

// SHA256Hex returns the hex encoded SHA-256 digest of a string.
func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// SecureCompare performs a constant time comparison (used for passwords,
// cluster keys and tokens).
func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// TokenPrefix is the human readable prefix of every API token.
const TokenPrefix = "up"

// NewAPIToken builds a token in the format up_<prefix>_<secret>.
//
// The returned prefix is stored in clear text (it only exists to show the
// token in the UI and to speed up lookups) while the hash covers the complete
// token, so a database leak cannot be replayed.
func NewAPIToken() (plain, prefix, hash string, err error) {
	prefix = MustRandomHex(4)
	secret, err := RandomHex(24)
	if err != nil {
		return "", "", "", err
	}
	plain = fmt.Sprintf("%s_%s_%s", TokenPrefix, prefix, secret)
	return plain, prefix, SHA256Hex(plain), nil
}

// APITokenPrefix extracts the lookup prefix from a presented token.
func APITokenPrefix(plain string) string {
	parts := strings.Split(plain, "_")
	if len(parts) != 3 || parts[0] != TokenPrefix {
		return ""
	}
	return parts[1]
}

// Sign returns the base64 HMAC-SHA256 signature of a message. It is used for
// the session cookies and the OIDC state, so no server side session storage is
// needed and every node of the cluster (sharing the same secret) validates the
// same cookie.
func Sign(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignedValue builds "payload.signature".
func SignedValue(secret, payload string) string {
	return payload + "." + Sign(secret, payload)
}

// VerifySignedValue checks the signature of a "payload.signature" value and
// returns the payload when it is valid.
func VerifySignedValue(secret, value string) (string, bool) {
	idx := strings.LastIndex(value, ".")
	if idx <= 0 {
		return "", false
	}
	payload, signature := value[:idx], value[idx+1:]
	if subtle.ConstantTimeCompare([]byte(signature), []byte(Sign(secret, payload))) != 1 {
		return "", false
	}
	return payload, true
}

// EncodeTime renders a UTC timestamp in the compact format used inside signed
// payloads.
func EncodeTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// DecodeTime parses a timestamp produced by EncodeTime.
func DecodeTime(raw string) (time.Time, error) {
	return time.Parse(time.RFC3339, raw)
}
