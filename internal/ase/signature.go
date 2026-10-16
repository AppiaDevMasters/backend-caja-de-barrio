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
// the two JavaScript line separators where JCS keeps them literal, so they are
// undone below, and Go formats floating point numbers its own way while webhook
// payloads carry amounts as strings.
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

	return stripNonJCSEscaping(canonical), nil
}

// lineSeparators are the two JavaScript line separators: Go escapes both and JCS
// keeps both literal.
var lineSeparators = []struct {
	escape  string
	literal string
}{
	{escape: `\u2028`, literal: "\u2028"},
	{escape: `\u2029`, literal: "\u2029"},
}

// stripNonJCSEscaping turns the escapes Go writes for the two line separators
// back into the literal characters and leaves every other byte alone.
//
// It walks the encoded JSON instead of replacing the escape as text, because a
// payload may carry that escape as TEXT: the characters \u2028 written in a
// payload are an escaped backslash followed by "u2028" and have to stay that way,
// or the digest would not match the one the sender computed.
func stripNonJCSEscaping(encoded []byte) []byte {
	needed := false
	for _, separator := range lineSeparators {
		if bytes.Contains(encoded, []byte(separator.escape)) {
			needed = true
			break
		}
	}
	if !needed {
		return encoded
	}

	stripped := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		// A doubled backslash is an escaped backslash, and the byte after it never
		// starts one of the escapes above.
		if encoded[i] == '\\' && i+1 < len(encoded) && encoded[i+1] == '\\' {
			stripped = append(stripped, encoded[i], encoded[i+1])
			i++
			continue
		}

		strippedHere := false
		for _, separator := range lineSeparators {
			if bytes.HasPrefix(encoded[i:], []byte(separator.escape)) {
				stripped = append(stripped, separator.literal...)
				i += len(separator.escape) - 1
				strippedHere = true
				break
			}
		}
		if strippedHere {
			continue
		}

		stripped = append(stripped, encoded[i])
	}

	return stripped
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
