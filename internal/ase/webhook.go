package ase

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// maxWebhookBytes keeps a misbehaving caller from filling memory. A real event
// carries one payment at most.
const maxWebhookBytes = 1 << 20

// handleWebhook receives the events Rafiki pushes to the backend.
//
// Rafiki treats any status but 200 as a failure and retries with backoff, so a
// signed event is acknowledged with exactly 200 and an empty body.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		s.logger.Warn("could not read the webhook body", "error", err)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	// Rafiki signs the canonical form of the parsed event, so the receiver has to
	// canonicalize what it parsed instead of hashing the bytes it received. The
	// decoder keeps numbers as they arrived, because re-encoding them is part of
	// rebuilding the bytes that were signed.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		s.logger.Warn("the webhook body is not a JSON object", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	canonical, err := canonicalJSON(body)
	if err != nil {
		s.logger.Warn("could not canonicalize the webhook body", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := verifySignature(r.Header.Get(signatureHeader), canonical, s.cfg, s.cfg.Now()); err != nil {
		s.logger.Warn("rejected a webhook", "error", err, "remote", r.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	s.logger.Info("received a webhook",
		"id", stringField(body, "id"),
		"type", stringField(body, "type"))

	w.WriteHeader(http.StatusOK)
}

// stringField reads a string field of the decoded event. A missing or mistyped
// field only leaves the log empty: the signature is the gate that accepted the
// event, and this is what the log says about it.
func stringField(body map[string]any, name string) string {
	value, _ := body[name].(string)

	return value
}
