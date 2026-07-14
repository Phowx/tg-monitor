package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestGenerateTokenReturnsBase64URLValueAndHash(t *testing.T) {
	randomBytes := bytes.Repeat([]byte{0x2a}, 32)
	raw, hash, err := GenerateToken(bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("DecodeString(%q) error = %v", raw, err)
	}
	if !bytes.Equal(decoded, randomBytes) {
		t.Fatalf("decoded token = %x, want %x", decoded, randomBytes)
	}
	if !bytes.Equal(hash, HashToken(raw)) {
		t.Fatalf("hash = %x, want %x", hash, HashToken(raw))
	}
	if len(hash) != 32 {
		t.Fatalf("hash length = %d, want 32", len(hash))
	}

	otherRaw, otherHash, err := GenerateToken(bytes.NewReader(bytes.Repeat([]byte{0x2b}, 32)))
	if err != nil {
		t.Fatalf("GenerateToken(second) error = %v", err)
	}
	if otherRaw == raw || bytes.Equal(otherHash, hash) {
		t.Fatal("GenerateToken() reused token material for different random input")
	}
}

func TestGenerateTokenPropagatesRandomSourceFailure(t *testing.T) {
	want := errors.New("random source failed")
	if _, _, err := GenerateToken(failingReader{err: want}); !errors.Is(err, want) {
		t.Fatalf("GenerateToken() error = %v, want wrapped %v", err, want)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}
