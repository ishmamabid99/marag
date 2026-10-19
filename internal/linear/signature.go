// Package linear is the Linear webhook trigger adapter: verify signature, parse
// the event, submit a run, and acknowledge on the issue.
package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrBadSignature is the sentinel wrapped by VerifySignature's errors so
// callers can route on identity (errors.Is) rather than message substrings.
var ErrBadSignature = errors.New("linear: bad signature")

// VerifySignature checks that signatureHeader is the hex HMAC-SHA256 of body
// under secret, using a constant-time compare. Returns nil iff valid.
func VerifySignature(body []byte, signatureHeader, secret string) error {
	if signatureHeader == "" {
		return fmt.Errorf("%w: missing", ErrBadSignature)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signatureHeader)) {
		return fmt.Errorf("%w: mismatch", ErrBadSignature)
	}
	return nil
}
