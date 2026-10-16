package rafikiadmin_test

import (
	"encoding/json"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

type amountHolder struct {
	Liquidity rafikiadmin.UInt64 `json:"liquidity"`
}

// Rafiki serializes its UInt64 scalars as JSON strings, because the value does
// not fit a JavaScript number. Found by running the seed against the real stack.
func TestUInt64DecodesTheStringRafikiSends(t *testing.T) {
	var got amountHolder
	if err := json.Unmarshal([]byte(`{"liquidity":"500000"}`), &got); err != nil {
		t.Fatalf("Unmarshal() returned an error: %v", err)
	}
	if got.Liquidity != 500000 {
		t.Errorf("Liquidity = %d, want 500000", got.Liquidity)
	}
}

// An asset with no liquidity yet comes back as null.
func TestUInt64DecodesNullAsZero(t *testing.T) {
	var got amountHolder
	if err := json.Unmarshal([]byte(`{"liquidity":null}`), &got); err != nil {
		t.Fatalf("Unmarshal() returned an error: %v", err)
	}
	if got.Liquidity != 0 {
		t.Errorf("Liquidity = %d, want 0", got.Liquidity)
	}
}

func TestUInt64DecodesAPlainNumber(t *testing.T) {
	var got amountHolder
	if err := json.Unmarshal([]byte(`{"liquidity":500000}`), &got); err != nil {
		t.Fatalf("Unmarshal() returned an error: %v", err)
	}
	if got.Liquidity != 500000 {
		t.Errorf("Liquidity = %d, want 500000", got.Liquidity)
	}
}

func TestUInt64RejectsSomethingThatIsNotAnAmount(t *testing.T) {
	var got amountHolder
	if err := json.Unmarshal([]byte(`{"liquidity":"quinientos mil"}`), &got); err == nil {
		t.Fatal("Unmarshal() accepted a non-numeric string, want an error")
	}
}

// Amounts travel back to Rafiki in the same form they arrive in.
func TestUInt64MarshalsAsAString(t *testing.T) {
	body, err := json.Marshal(struct {
		Amount rafikiadmin.UInt64 `json:"amount"`
	}{Amount: 500000})
	if err != nil {
		t.Fatalf("Marshal() returned an error: %v", err)
	}
	if want := `{"amount":"500000"}`; string(body) != want {
		t.Errorf("Marshal() = %s, want %s", body, want)
	}
}
