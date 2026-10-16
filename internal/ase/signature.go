package ase

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// signatureHeader is the header Rafiki signs webhooks with. It differs from the
// Admin API, which uses "Signature" and the tenant API secret:
// generateWebhookSignature in the webhook service of Rafiki builds this one.
const signatureHeader = "Rafiki-Signature"

// canonicalJSON renders a value the way the canonicalizer Rafiki hashes with
// does: sorted object keys, no whitespace and literal UTF-8.
//
// Go sorts map keys and can turn HTML escaping off, which is most of JCS. Two
// differences remain, and only the first one can appear in a webhook: Go escapes
// the two JavaScript line separators where JCS keeps them literal, so the escape
// is undone below, and Go formats floating point numbers its own way while
// webhook payloads carry amounts as strings.
//
// Numbers arrive as json.Number, because the decoder that reads the body keeps
// them exactly as they were sent.
func canonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("canonicalizing the JSON body: %w", err)
	}

	canonical := bytes.TrimRight(buf.Bytes(), "\n")
	canonical = bytes.ReplaceAll(canonical, []byte(`\u2028`), []byte("\u2028"))
	canonical = bytes.ReplaceAll(canonical, []byte(`\u2029`), []byte("\u2029"))

	return canonical, nil
}

// verifySignature checks a Rafiki-Signature header
//
//	Rafiki-Signature: t=<unix milliseconds>, v<version>=<hex HMAC-SHA256>
//
// against the canonical body, which is what Rafiki hashed:
// HMAC-SHA256(secret, "<timestamp>.<canonical body>").
//
// Every rejection returns an error the receiver logs; the caller answers 401
// without saying which part failed.
func verifySignature(header string, canonicalBody []byte, cfg Config, now time.Time) error {
	timestampPart, digestPart, found := strings.Cut(header, ", ")
	if !found {
		return errors.New("the signature header has no \", \" separator")
	}

	rawTimestamp, found := strings.CutPrefix(timestampPart, "t=")
	if !found {
		return fmt.Errorf("the signature timestamp %q has no t= prefix", timestampPart)
	}

	digest, found := strings.CutPrefix(digestPart, "v"+strconv.Itoa(cfg.SignatureVersion)+"=")
	if !found {
		return fmt.Errorf("the signature digest %q is not a v%d digest", digestPart, cfg.SignatureVersion)
	}

	milliseconds, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("the signature timestamp %q is not unix milliseconds", rawTimestamp)
	}
	if age := now.Sub(time.UnixMilli(milliseconds)); age > cfg.MaxSignatureAge || age < -cfg.MaxSignatureAge {
		return fmt.Errorf("the signature is %v away from now, beyond the %v window", age.Round(time.Second), cfg.MaxSignatureAge)
	}

	received, err := hex.DecodeString(digest)
	if err != nil {
		return fmt.Errorf("the signature digest %q is not hexadecimal", digest)
	}

	mac := hmac.New(sha256.New, []byte(cfg.SignatureSecret))
	mac.Write([]byte(rawTimestamp + "."))
	mac.Write(canonicalBody)
	if !hmac.Equal(mac.Sum(nil), received) {
		return errors.New("the signature does not cover this body")
	}

	return nil
}
