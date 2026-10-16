package rafikiadmin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// UInt64 is a Rafiki unsigned 64-bit amount: liquidity balances, thresholds and
// payment amounts.
//
// Rafiki serializes these scalars as JSON strings, because the value does not fit
// in a JavaScript number, so this type reads the string form, tolerates the plain
// number form and writes amounts back as strings.
type UInt64 int64

// UnmarshalJSON accepts "500000", 500000 and null.
func (u *UInt64) UnmarshalJSON(data []byte) error {
	text := string(bytes.Trim(data, `"`))
	if text == "" || text == "null" {
		*u = 0
		return nil
	}

	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("rafikiadmin: %q is not an unsigned 64-bit amount: %w", text, err)
	}
	*u = UInt64(value)

	return nil
}

// MarshalJSON writes the amount in the same form Rafiki sends it.
func (u UInt64) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(u), 10))
}
