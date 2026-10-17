package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
)

// AvisosAgente sends notices to the agent's POST /internal/avisos, which tells the
// person in her own conversation.
type AvisosAgente struct {
	URL    string
	Token  string
	Client *http.Client
}

// Avisar sends one notice.
func (a AvisosAgente) Avisar(ctx context.Context, whatsapp, texto string) error {
	cuerpo, err := json.Marshal(map[string]string{"whatsapp": whatsapp, "aviso": texto})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(a.URL, "/")+"/internal/avisos",
		bytes.NewReader(cuerpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.ServiceTokenHeader, a.Token)

	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("admin: calling the agent: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("admin: the agent answered %d", res.StatusCode)
	}

	return nil
}
