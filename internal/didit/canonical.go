package didit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// canonicalJSON renders a webhook body the way Didit's canonicalizer does before
// it signs:
//
//	json.dumps(body, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
//
// with one extra step from its shorten_floats helper: a whole-valued float is
// written as an integer, so a body choosing to send 1.0 and one choosing to send
// 1 produce the same bytes, and therefore the same signature.
//
// Go covers most of that already: it sorts map keys, and turning HTML escaping
// off leaves <, > and & literal, which is what ensure_ascii=False does. Two
// differences are left, and only the first can appear in a webhook: Go escapes
// U+2028 and U+2029 for JavaScript safety where the canonical form keeps them
// literal, and Go formats floats its own way while the canonical form shortens
// whole ones.
//
// Every number arrives as a json.Number, so a value bigger than a float64 can
// hold keeps its digits instead of being rounded.
func canonicalJSON(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("didit: decoding the webhook body: %w", err)
	}
	// A body carries exactly one JSON document. A second one would sit outside
	// what the sender signed, so it is refused rather than ignored.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("didit: the webhook body carries more than one JSON document")
	}

	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("didit: the webhook body is not a JSON object")
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(shortenNumbers(object)); err != nil {
		return nil, fmt.Errorf("didit: canonicalizing the webhook body: %w", err)
	}

	// Encode appends a newline that the canonical form does not carry.
	return stripNonCanonicalEscaping(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// shortenNumbers walks the decoded body and rewrites every whole-valued float as
// an integer, the way the sender's canonicalizer does before signing.
func shortenNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, entry := range typed {
			typed[key] = shortenNumbers(entry)
		}

		return typed
	case []any:
		for index, entry := range typed {
			typed[index] = shortenNumbers(entry)
		}

		return typed
	case json.Number:
		return shortenNumber(typed)
	default:
		return value
	}
}

// shortenNumber rewrites one number. An integer as written is returned untouched,
// because passing it through a float64 would round an identifier that is larger
// than 2^53 and the signature covers the digits as sent.
func shortenNumber(number json.Number) any {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		// An integer as written is returned untouched: routing it through a
		// float64 would round an identifier larger than 2^53, and the signature
		// covers the digits as sent. Python agrees except for one case.
		if text == "-0" {
			// Python has no negative zero int: json.loads("-0") is 0.
			return json.Number("0")
		}

		return number
	}

	value, err := number.Float64()
	if err != nil {
		// Not a form this package understands: keep it as it arrived rather than
		// inventing a different number.
		return number
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return number
	}
	if value == math.Trunc(value) {
		// The sender's canonicalizer turns a whole-valued float into an int, and
		// Python's int is exact: int(1e30) is the binary value the float holds,
		// not the shortest decimal that reads back as it. The two differ from
		// 1e23 upwards, and 1e30 is the case the tests pin.
		if integer, accuracy := new(big.Float).SetFloat64(value).Int(nil); accuracy == big.Exact {
			return json.Number(integer.String())
		}
	}

	return json.Number(pythonFloat(value))
}

// pythonFloat renders a non-integral float the way Python's repr does, because
// that is exactly what the sender's json.dumps writes into the bytes it signs:
// the shortest decimal that round-trips, written positionally while the decimal
// exponent stays within [-4, 16) and in exponent form outside that window.
//
// Go's own %g format switches to exponent form at an exponent of 6, so a body
// carrying 1234567.5 would canonicalise to 1.2345675e+06 here while the sender
// signed 1234567.5: a valid delivery rejected with a silent 401.
func pythonFloat(value float64) string {
	exponential := strconv.FormatFloat(value, 'e', -1, 64)

	_, exponentText, found := strings.Cut(exponential, "e")
	if !found {
		return exponential
	}
	exponent, err := strconv.Atoi(exponentText)
	if err != nil {
		return exponential
	}
	if exponent < -4 || exponent >= 16 {
		return exponential
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
}

// lineSeparators are the two JavaScript line separators: Go escapes both and the
// canonical form keeps both literal.
var lineSeparators = []struct {
	escape  string
	literal string
}{
	{escape: `\u2028`, literal: "\u2028"},
	{escape: `\u2029`, literal: "\u2029"},
}

// stripNonCanonicalEscaping turns the escapes Go writes for the two line
// separators back into the literal characters and leaves every other byte alone.
//
// It walks the encoded JSON instead of replacing the escape as text, because a
// body may carry that escape as TEXT: the characters \u2028 written in a body are
// an escaped backslash followed by "u2028" and have to stay that way, or the
// digest would not match the one the sender computed.
//
// internal/ase keeps its own copy of this walk for the Rafiki webhook signature,
// which has the same requirement. Sharing it would mean a third package that
// neither task owns, so the duplication is deliberate and both copies are covered
// by the same cases.
func stripNonCanonicalEscaping(encoded []byte) []byte {
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
