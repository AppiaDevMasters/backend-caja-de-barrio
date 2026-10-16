package ase_test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// getRates is the call Rafiki makes: a GET with the base asset code as a query
// parameter, and any status but 200 is a failure for it.
func getRates(t *testing.T, handler http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/rates"+query, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// ratesReply is the body Rafiki reads: {base, rates} with a price per target
// asset code.
type ratesReply struct {
	Base  string             `json:"base"`
	Rates map[string]float64 `json:"rates"`
}

func decodeRates(t *testing.T, rec *httptest.ResponseRecorder) ratesReply {
	t.Helper()

	if contentType := rec.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	var reply ratesReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("the reply %q is not the rates JSON Rafiki reads: %v", rec.Body.String(), err)
	}

	return reply
}

// Rafiki validates every price before using it: a number, finite and greater
// than zero. A string amount would be refused, unlike the UInt64 amounts of the
// Admin API.
func requireRafikiPrices(t *testing.T, rates map[string]float64) {
	t.Helper()

	if len(rates) == 0 {
		t.Fatal("rates is empty, want at least one price")
	}
	for code, price := range rates {
		if math.IsInf(price, 0) || math.IsNaN(price) || price <= 0 {
			t.Errorf("rates[%q] = %v, want a finite price above zero", code, price)
		}
	}
}

func TestRatesServesThePricesOfTheBaseAsset(t *testing.T) {
	rec := getRates(t, newHandler(t), "?base=USD")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%q", rec.Code, rec.Body.String())
	}

	reply := decodeRates(t, rec)
	if reply.Base != "USD" {
		t.Errorf("base = %q, want the requested USD", reply.Base)
	}
	if got := reply.Rates["COP"]; got != 4000 {
		t.Errorf("rates[COP] = %v, want 4000", got)
	}
	requireRafikiPrices(t, reply.Rates)
}

// Rafiki asks with the asset code it stores, and the lookup does not depend on
// the case the caller used.
func TestRatesLooksUpTheBaseAssetIgnoringCase(t *testing.T) {
	rec := getRates(t, newHandler(t), "?base=usd")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%q", rec.Code, rec.Body.String())
	}
	if reply := decodeRates(t, rec); reply.Rates["COP"] != 4000 {
		t.Errorf("rates = %v, want the COP price of the USD base", reply.Rates)
	}
}

func TestRatesServesTheOtherDirection(t *testing.T) {
	rec := getRates(t, newHandler(t), "?base=COP")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%q", rec.Code, rec.Body.String())
	}

	reply := decodeRates(t, rec)
	if reply.Base != "COP" {
		t.Errorf("base = %q, want the requested COP", reply.Base)
	}
	if got := reply.Rates["USD"]; got != 0.00025 {
		t.Errorf("rates[USD] = %v, want 0.00025", got)
	}
	requireRafikiPrices(t, reply.Rates)
}

// Rafiki always sends the base, so a request without one is a caller error and
// saying so is more useful than an empty table.
func TestRatesRejectsARequestWithoutABaseAsset(t *testing.T) {
	tests := map[string]string{
		"no parameter":  "",
		"empty value":   "?base=",
		"blank value":   "?base=%20",
		"unknown asset": "?base=XYZ",
	}

	want := map[string]int{
		"no parameter":  http.StatusBadRequest,
		"empty value":   http.StatusBadRequest,
		"blank value":   http.StatusBadRequest,
		"unknown asset": http.StatusNotFound,
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			rec := getRates(t, newHandler(t), query)
			if rec.Code != want[name] {
				t.Errorf("status = %d, want %d: body=%q", rec.Code, want[name], rec.Body.String())
			}
		})
	}
}

// Rafiki reads the reply as JSON, so an unmatched route or method must not
// answer with the rates of another request.
func TestRatesOnlyAnswersGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/rates?base=USD", nil)
	rec := httptest.NewRecorder()
	newHandler(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
