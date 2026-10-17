package ase

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// identitySecretHeader is the header the authorization server authenticates the
// identity provider with.
const identitySecretHeader = "x-idp-secret"

// maxInteractionBytes keeps a misbehaving authorization server from filling
// memory: the details of one grant are a few hundred bytes.
const maxInteractionBytes = 1 << 20

// Consent form fields and the two choices the authorization server accepts.
const (
	formFieldInteractID = "interactId"
	formFieldNonce      = "nonce"
	formFieldChoice     = "choice"

	choiceAccept = "accept"
	choiceReject = "reject"
)

// interactionDetails is what the authorization server reports about the grant an
// interaction belongs to.
type interactionDetails struct {
	GrantID string           `json:"grantId"`
	State   string           `json:"state"`
	Access  []map[string]any `json:"access"`
}

// consentPage is what the screen renders.
type consentPage struct {
	ClientName string
	ClientURI  string
	GrantID    string
	State      string
	Access     []string
	InteractID string
	Nonce      string
}

// handleIDPPage renders the consent screen of an interactive grant. The
// authorization server sends the browser here with the interaction id, its nonce
// and who is asking, so the page can show what is about to be approved.
func (s *Server) handleIDPPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	interactID := strings.TrimSpace(query.Get(formFieldInteractID))
	nonce := strings.TrimSpace(query.Get(formFieldNonce))
	if interactID == "" || nonce == "" {
		s.logger.Warn("rejected an IdP request", "error", "the interaction id and nonce are required")
		http.Error(w, "the interaction id and nonce are required", http.StatusBadRequest)
		return
	}
	if s.cfg.InteractionServerURL == "" || s.cfg.AuthServerURL == "" {
		s.logger.Error("the identity provider has no authorization server to talk to")
		http.Error(w, "the identity provider is not configured", http.StatusServiceUnavailable)
		return
	}

	details, err := s.interactionDetails(r.Context(), interactID, nonce)
	if err != nil {
		s.logger.Error("could not read the interaction", "interaction", interactID, "error", err)
		http.Error(w, "the authorization server refused the interaction", http.StatusBadGateway)
		return
	}

	page := consentPage{
		ClientName: query.Get("clientName"),
		ClientURI:  query.Get("clientUri"),
		GrantID:    details.GrantID,
		State:      details.State,
		Access:     accessLines(details.Access),
		InteractID: interactID,
		Nonce:      nonce,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := consentTemplate.Execute(w, page); err != nil {
		// The status line is already written, so the only thing left is a log.
		s.logger.Error("could not render the consent page", "error", err)
	}
}

// handleIDPChoice records the user's decision with the authorization server and
// then sends the browser to finish the interaction there: the callback hash is
// computed by that server, not here.
func (s *Server) handleIDPChoice(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.logger.Warn("rejected an IdP choice", "error", err)
		http.Error(w, "the choice could not be read", http.StatusBadRequest)
		return
	}

	interactID := strings.TrimSpace(r.PostForm.Get(formFieldInteractID))
	nonce := strings.TrimSpace(r.PostForm.Get(formFieldNonce))
	choice := strings.TrimSpace(r.PostForm.Get(formFieldChoice))
	if interactID == "" || nonce == "" {
		s.logger.Warn("rejected an IdP choice", "error", "the interaction id and nonce are required")
		http.Error(w, "the interaction id and nonce are required", http.StatusBadRequest)
		return
	}
	if choice != choiceAccept && choice != choiceReject {
		s.logger.Warn("rejected an IdP choice", "error", "the choice is not accept or reject", "choice", choice)
		http.Error(w, "the choice is not accept or reject", http.StatusBadRequest)
		return
	}
	if s.cfg.InteractionServerURL == "" || s.cfg.AuthServerURL == "" {
		s.logger.Error("the identity provider has no authorization server to talk to")
		http.Error(w, "the identity provider is not configured", http.StatusServiceUnavailable)
		return
	}

	if err := s.interactionChoice(r.Context(), interactID, nonce, choice); err != nil {
		s.logger.Error("could not record the interaction choice", "interaction", interactID, "choice", choice, "error", err)
		http.Error(w, "the authorization server refused the interaction", http.StatusBadGateway)
		return
	}

	http.Redirect(w, r, s.finishInteractionURL(interactID, nonce), http.StatusSeeOther)
}

// interactionDetails reads the grant the interaction belongs to, server to server.
func (s *Server) interactionDetails(ctx context.Context, interactID, nonce string) (interactionDetails, error) {
	endpoint := s.interactionURL(interactID, nonce)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return interactionDetails{}, fmt.Errorf("building the interaction request: %w", err)
	}
	req.Header.Set(identitySecretHeader, s.cfg.IdentityServerSecret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return interactionDetails{}, fmt.Errorf("calling the authorization server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return interactionDetails{}, fmt.Errorf("the authorization server answered %d %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var details interactionDetails
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxInteractionBytes)).Decode(&details); err != nil {
		return interactionDetails{}, fmt.Errorf("reading the interaction details: %w", err)
	}

	return details, nil
}

// interactionChoice records the user's decision. The authorization server answers
// 202 and no body.
func (s *Server) interactionChoice(ctx context.Context, interactID, nonce, choice string) error {
	endpoint := s.interactionURL(interactID, nonce) + "/" + choice

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building the choice request: %w", err)
	}
	req.Header.Set(identitySecretHeader, s.cfg.IdentityServerSecret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling the authorization server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("the authorization server answered %d %s",
			resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	return nil
}

// interactionURL is the BACK-CHANNEL endpoint of one interaction, as the IdP reaches it
// server to server: the interaction server of the authorization server, which is the one that
// reads grant information and resolves a choice.
//
// It is NOT the front-channel path a browser opens (/interact/{id}/{nonce} on the
// interaction server). The IdP used to call that one, and the authorization server
// answered 404, which the page turned into a 502.
func (s *Server) interactionURL(interactID, nonce string) string {
	return strings.TrimSuffix(s.cfg.InteractionServerURL, "/") +
		"/grant/" + url.PathEscape(interactID) + "/" + url.PathEscape(nonce)
}

// finishInteractionURL is where the browser continues after choosing: the
// authorization server finishes the grant there and redirects to the client with
// the hash and the interaction reference.
func (s *Server) finishInteractionURL(interactID, nonce string) string {
	base := s.cfg.AuthServerPublicURL
	if base == "" {
		base = s.cfg.AuthServerURL
	}

	return strings.TrimSuffix(base, "/") +
		"/interact/" + url.PathEscape(interactID) + "/" + url.PathEscape(nonce) + "/finish"
}

// accessLines renders each access the client asks for as one line of the screen:
// what it is, over which address and, when it carries limits, the values the user
// is approving.
func accessLines(access []map[string]any) []string {
	lines := make([]string, 0, len(access))

	for _, entry := range access {
		line := stringField(entry, "type")
		if identifier := stringField(entry, "identifier"); identifier != "" {
			line += " · " + identifier
		}

		limits, ok := entry["limits"].(map[string]any)
		if !ok || len(limits) == 0 {
			lines = append(lines, line)
			continue
		}

		keys := make([]string, 0, len(limits))
		for key := range limits {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s: %v", key, limits[key]))
		}
		lines = append(lines, line+" · "+strings.Join(parts, ", "))
	}

	return lines
}

// consentHTML is the screen a person sees before approving a grant.
const consentHTML = `<!doctype html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Aprobar acceso</title>
</head>
<body>
<h1>Aprobar acceso</h1>
<p>{{if .ClientName}}<strong>{{.ClientName}}</strong>{{else}}Un cliente{{end}}{{if .ClientURI}} ({{.ClientURI}}){{end}} pide autorizacion para operar en tu nombre.</p>
<p>Solicitud: {{.GrantID}} · estado: {{.State}}</p>
{{if .Access}}<ul>{{range .Access}}<li>{{.}}</li>{{end}}</ul>{{end}}
<form method="post" action="/idp/approve">
<input type="hidden" name="interactId" value="{{.InteractID}}">
<input type="hidden" name="nonce" value="{{.Nonce}}">
<button type="submit" name="choice" value="accept">Aprobar</button>
<button type="submit" name="choice" value="reject">Rechazar</button>
</form>
</body>
</html>
`

var consentTemplate = template.Must(template.New("consent").Parse(consentHTML))
