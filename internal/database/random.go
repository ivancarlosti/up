package database

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/google/uuid"
)

// randomHex returns n random bytes as a hex string.
func randomHex(n int) (string, error) {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// newUUID returns a version 4 UUID (used for the cluster private key).
func newUUID() string {
	return uuid.NewString()
}
