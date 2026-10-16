package ase

import (
	"net/http"
	"strings"
)

// ratesBaseParam is the query parameter Rafiki sends: the asset code it wants
// prices for.
const ratesBaseParam = "base"

// demoPrices says how many units of each target asset one unit of the base asset
// buys. Rafiki reads rates[<destination asset code>] when a quote converts
// between two different currencies, and refuses a price that is not a finite
// number above zero.
//
// The pair below is the hackathon demo table, kept explicit so nobody mistakes
// it for a market feed: 1 USD = 4000 COP. Replacing it with a real source only
// has to keep this shape.
var demoPrices = map[string]map[string]float64{
	"COP": {"USD": 0.00025},
	"USD": {"COP": 4000},
}

// ratesReply is the body Rafiki parses: the base asset code it validates is a
// string, plus one price per target asset code.
type ratesReply struct {
	Base  string             `json:"base"`
	Rates map[string]float64 `json:"rates"`
}

// handleRates answers the exchange rates endpoint Rafiki calls as
// GET <EXCHANGE_RATES_URL>?base=<CODE>. Rafiki only accepts status 200, so an
// answer always carries the base it validated and at least one price.
func (s *Server) handleRates(w http.ResponseWriter, r *http.Request) {
	base := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get(ratesBaseParam)))
	if base == "" {
		s.logger.Warn("rejected a rates request", "error", "the base query parameter is missing")
		http.Error(w, "the base query parameter is required", http.StatusBadRequest)
		return
	}

	prices, found := demoPrices[base]
	if !found {
		s.logger.Warn("rejected a rates request", "error", "the ASE prices no such base asset", "base", base)
		http.Error(w, "the ASE prices no such base asset", http.StatusNotFound)
		return
	}

	s.writeJSON(w, http.StatusOK, ratesReply{Base: base, Rates: prices})
}
