// Package admin serves the control panel: /admin/* with a session cookie, CORS only
// for the panel's own origins, a role per route and an audit line for every write
// (panel-de-control-v3.md, sections 3.1 and 3.2).
//
// The panel never reaches /internal and never holds the service token: everything it
// can do goes through here, as the operator who signed in.
package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// CookieSesion is the name of the session cookie.
const CookieSesion = "cdb_sesion"

// duracionSesion is how long a sign-in lasts: one shift.
const duracionSesion = 8 * time.Hour

// motivoMinimo is the shortest reason a write accepts.
const motivoMinimo = 10

// Datos is what the panel reads and writes in the database.
type Datos interface {
	UsuarioPanelPorUsername(ctx context.Context, username string) (store.UsuarioPanel, bool, error)
	Auditar(ctx context.Context, entrada store.Auditoria) error
	HistorialDe(ctx context.Context, entidad, entidadID string) ([]store.EntradaHistorial, error)
	PersonasPanel(ctx context.Context, estado, texto string) ([]store.PersonaPanel, error)
	PersonaPanelPorID(ctx context.Context, id string) (store.PersonaPanel, bool, error)
	CambiarEstadoPersona(ctx context.Context, id string, desde []string, a string) (bool, error)
	AsignarWallet(ctx context.Context, personaID, wallet string) error
	MovimientosDe(ctx context.Context, wallet string) (store.Billetera, error)
	RegistrarMovimiento(ctx context.Context, id, wallet, tipo string, monto int64, referencia string) error
	PersonaPorWhatsapp(ctx context.Context, whatsapp string) (store.Persona, bool, error)
	CrearTiendaPendiente(ctx context.Context, tienda store.TiendaNueva) error
	TiendasPanel(ctx context.Context, estado, texto string) ([]store.TiendaPanel, error)
	TiendaPanelPorID(ctx context.Context, id string) (store.TiendaPanel, bool, error)
	CambiarEstadoTienda(ctx context.Context, id string, desde []string, a string) (bool, error)
}

// Billeteras creates the Open Payments wallet of a person approved by hand.
type Billeteras interface {
	CrearBilletera(ctx context.Context, personaID, nombre string) (string, error)
}

// Avisos tells a person something through the agent.
type Avisos interface {
	Avisar(ctx context.Context, whatsapp, texto string) error
}

// Config is what the panel surface needs besides its data.
type Config struct {
	// Origenes are the exact origins allowed by CORS (PANEL_ORIGINS).
	Origenes []string
	// Secreto signs the session cookie.
	Secreto []byte
	// SaldoInicial is the load of a wallet approved by hand (SALDO_INICIAL_DEMO_COP).
	SaldoInicial int64
	// CookieInsegura drops Secure from the cookie. Only for tests over plain HTTP.
	CookieInsegura bool
}

// Server is the /admin surface.
type Server struct {
	cfg        Config
	datos      Datos
	billeteras Billeteras
	avisos     Avisos
	mux        *http.ServeMux
}

// New builds the surface. billeteras and avisos may be nil.
func New(cfg Config, datos Datos, billeteras Billeteras, avisos Avisos) (*Server, error) {
	if len(cfg.Secreto) == 0 {
		return nil, errors.New("admin: the session secret is empty")
	}
	s := &Server{cfg: cfg, datos: datos, billeteras: billeteras, avisos: avisos, mux: http.NewServeMux()}

	s.mux.HandleFunc("POST /admin/auth/login", s.login)
	s.mux.HandleFunc("POST /admin/auth/logout", s.logout)
	s.mux.HandleFunc("GET /admin/auth/yo", s.conSesion(rolSoporte, s.yo))

	s.mux.HandleFunc("GET /admin/personas", s.conSesion(rolSoporte, s.listarPersonas))
	s.mux.HandleFunc("GET /admin/personas/{id}", s.conSesion(rolSoporte, s.fichaPersona))
	s.mux.HandleFunc("POST /admin/personas/{id}/aprobar", s.conSesion(rolCumplimiento, s.aprobarPersona))
	s.mux.HandleFunc("POST /admin/personas/{id}/rechazar", s.conSesion(rolCumplimiento, s.rechazarPersona))

	s.mux.HandleFunc("POST /admin/tenderos", s.conSesion(rolCumplimiento, s.registrarTendero))
	s.mux.HandleFunc("GET /admin/tiendas", s.conSesion(rolSoporte, s.listarTiendas))
	s.mux.HandleFunc("GET /admin/tiendas/{id}", s.conSesion(rolSoporte, s.fichaTienda))
	s.mux.HandleFunc("POST /admin/tiendas/{id}/estado", s.conSesion(rolCumplimiento, s.cambiarEstadoTienda))

	return s, nil
}

// Register mounts /admin on the main mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.Handle("/admin/", s.Handler())
}

// Handler is the surface with CORS around it.
func (s *Server) Handler() http.Handler {
	return s.cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := s.mux.Handler(r); pattern == "" {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "Esa ruta no existe en el panel."))

			return
		}
		s.mux.ServeHTTP(w, r)
	}))
}

// cors answers only the panel's own origins, never "*": the session travels in a cookie.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origen := r.Header.Get("Origin")
		permitido := false
		for _, o := range s.cfg.Origenes {
			if origen != "" && origen == o {
				permitido = true

				break
			}
		}
		w.Header().Add("Vary", "Origin")
		if permitido {
			w.Header().Set("Access-Control-Allow-Origin", origen)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			if permitido {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}

			return
		}
		next.ServeHTTP(w, r)
	})
}

// Roles, from least to most.
const (
	rolSoporte = iota
	rolCumplimiento
	rolSuperadmin
)

func nivelDe(rol string) int {
	switch rol {
	case "superadmin":
		return rolSuperadmin
	case "cumplimiento":
		return rolCumplimiento
	case "soporte":
		return rolSoporte
	default:
		return -1
	}
}

// Sesion is the signed content of the cookie.
type Sesion struct {
	Username string `json:"u"`
	Nombre   string `json:"n"`
	Rol      string `json:"r"`
	Vence    int64  `json:"v"`
}

func (s *Server) firmar(sesion Sesion) string {
	contenido, _ := json.Marshal(sesion)
	cuerpo := base64.RawURLEncoding.EncodeToString(contenido)
	mac := hmac.New(sha256.New, s.cfg.Secreto)
	mac.Write([]byte(cuerpo))

	return cuerpo + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) leerSesion(r *http.Request) (Sesion, bool) {
	cookie, err := r.Cookie(CookieSesion)
	if err != nil {
		return Sesion{}, false
	}
	cuerpo, firma, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return Sesion{}, false
	}
	recibida, err := base64.RawURLEncoding.DecodeString(firma)
	if err != nil {
		return Sesion{}, false
	}
	mac := hmac.New(sha256.New, s.cfg.Secreto)
	mac.Write([]byte(cuerpo))
	if !hmac.Equal(recibida, mac.Sum(nil)) {
		return Sesion{}, false
	}
	contenido, err := base64.RawURLEncoding.DecodeString(cuerpo)
	if err != nil {
		return Sesion{}, false
	}
	var sesion Sesion
	if json.Unmarshal(contenido, &sesion) != nil || time.Now().Unix() >= sesion.Vence || nivelDe(sesion.Rol) < 0 {
		return Sesion{}, false
	}

	return sesion, true
}

func (s *Server) ponerCookie(w http.ResponseWriter, valor string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieSesion,
		Value:    valor,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   !s.cfg.CookieInsegura,
		SameSite: http.SameSiteNoneMode,
	})
}

type claveSesion struct{}

// conSesion requires a valid cookie and at least the given role.
func (s *Server) conSesion(minimo int, next func(http.ResponseWriter, *http.Request, Sesion)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sesion, ok := s.leerSesion(r)
		if !ok {
			api.Fail(w, api.Errorf(api.CodeNoAutorizado, "Tu sesión venció. Inicia sesión de nuevo."))

			return
		}
		if nivelDe(sesion.Rol) < minimo {
			api.Fail(w, api.Errorf(api.CodeSinPermiso, "Tu rol no tiene permiso para esta acción."))

			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), claveSesion{}, sesion)), sesion)
	}
}

type usuarioBody struct {
	Username string `json:"username"`
	Nombre   string `json:"nombre"`
	Rol      string `json:"rol"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		strings.TrimSpace(body.Username) == "" || body.Password == "" {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Escribe tu usuario y tu contraseña."))

		return
	}

	usuario, ok, err := s.datos.UsuarioPanelPorUsername(r.Context(), strings.TrimSpace(body.Username))
	if err != nil {
		api.Fail(w, err)

		return
	}
	// The same answer for an unknown user and a wrong password, so the panel does not
	// tell anyone which usernames exist.
	if !ok || bcrypt.CompareHashAndPassword([]byte(usuario.ClaveHash), []byte(body.Password)) != nil {
		slog.Warn("panel: rejected a sign-in", "username", body.Username)
		api.Fail(w, api.Errorf(api.CodeNoAutorizado, "Usuario o contraseña incorrectos."))

		return
	}

	sesion := Sesion{Username: usuario.Username, Nombre: usuario.Nombre, Rol: usuario.Rol,
		Vence: time.Now().Add(duracionSesion).Unix()}
	s.ponerCookie(w, s.firmar(sesion), int(duracionSesion.Seconds()))
	api.JSON(w, http.StatusOK, usuarioBody{Username: usuario.Username, Nombre: usuario.Nombre, Rol: usuario.Rol})
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	s.ponerCookie(w, "", -1)
	api.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) yo(w http.ResponseWriter, _ *http.Request, sesion Sesion) {
	api.JSON(w, http.StatusOK, usuarioBody{Username: sesion.Username, Nombre: sesion.Nombre, Rol: sesion.Rol})
}

// leerMotivo decodes a write's body and requires its reason.
func leerMotivo(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Motivo string `json:"motivo"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido: se espera motivo."))

		return "", false
	}
	motivo := strings.TrimSpace(body.Motivo)
	if utf8.RuneCountInString(motivo) < motivoMinimo {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El motivo debe tener al menos %d caracteres.", motivoMinimo))

		return "", false
	}

	return motivo, true
}

// auditar records a write. A failure is logged and does not undo the action, which
// already happened.
func (s *Server) auditar(ctx context.Context, sesion Sesion, accion, entidad, entidadID, motivo string, detalle map[string]any) {
	id, err := nuevoID("aud_")
	if err == nil {
		err = s.datos.Auditar(ctx, store.Auditoria{ID: id, Actor: sesion.Username, Accion: accion, Entidad: entidad,
			EntidadID: entidadID, Motivo: motivo, Detalle: detalle})
	}
	if err != nil {
		slog.Error("panel: recording the audit line", "accion", accion, "entidad_id", entidadID, "error", err)
	}
}

// avisar tells the person through the agent, in the background: a slow agent must not
// hold the operator's request, and a failed notice does not undo the decision.
func (s *Server) avisar(whatsapp, texto string) {
	if s.avisos == nil || whatsapp == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.avisos.Avisar(ctx, whatsapp, texto); err != nil {
			slog.Error("panel: notifying the person through the agent", "error", err)
		}
	}()
}

func nuevoID(prefijo string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return prefijo + hex.EncodeToString(b), nil
}

// HashClave hashes a panel password for usuarios_panel.
func HashClave(clave string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(clave), bcrypt.DefaultCost)

	return string(hash), err
}
