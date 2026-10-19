package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func sign(body []byte, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"create"}`)
	secret := "s3cr3t"
	good := sign(body, secret)

	if err := VerifySignature(body, good, secret); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifySignature(body, "deadbeef", secret); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("bad signature: err = %v, want errors.Is(err, ErrBadSignature)", err)
	}
	if err := VerifySignature(body, "", secret); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("empty signature: err = %v, want errors.Is(err, ErrBadSignature)", err)
	}
	if err := VerifySignature([]byte(`{"action":"tampered"}`), good, secret); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered body: err = %v, want errors.Is(err, ErrBadSignature)", err)
	}
}
