package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

func GenerateToken(random io.Reader) (string, []byte, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", nil, fmt.Errorf("generate agent token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	return raw, HashToken(raw), nil
}

func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return append([]byte(nil), sum[:]...)
}
