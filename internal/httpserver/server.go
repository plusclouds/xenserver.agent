// Package httpserver exposes the agent's command dispatcher over HTTPS.
// It is an optional, additional transport next to NATS: the same envelope
// goes in, the same result envelope comes out, and the same allowed_operations
// allowlist applies. Disabled by default.
package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/config"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// Dispatcher is the subset of dispatcher.Dispatcher the server needs.
type Dispatcher interface {
	Dispatch(ctx context.Context, env protocol.Envelope) protocol.Envelope
}

// Server is the HTTPS command endpoint.
type Server struct {
	cfg       config.HTTPConfig
	disp      Dispatcher
	token     []byte
	agentUUID string
	logger    *zap.Logger
	srv       *http.Server
}

// New builds the server. token is the bearer token callers must present
// (the agent API key). It must not be empty.
func New(cfg config.HTTPConfig, disp Dispatcher, token, agentUUID string, logger *zap.Logger) (*Server, error) {
	if token == "" {
		return nil, errors.New("http server requires a non-empty api_key for bearer authentication")
	}
	if err := CheckToken(token); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, disp: disp, token: []byte(token), agentUUID: agentUUID, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/command", s.auth(s.handleCommand))
	s.srv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Start loads (or generates) the certificate and serves in the background.
// It returns after the certificate is ready; serve errors are logged.
func (s *Server) Start() error {
	cert, selfSigned, err := LoadOrCreateCert(s.cfg.CertFile, s.cfg.KeyFile, s.cfg.SelfSigned, s.cfg.Hosts)
	if err != nil {
		return err
	}
	s.srv.TLSConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	fp := sha256.Sum256(cert.Certificate[0])
	s.logger.Info("HTTPS server starting",
		zap.String("listen", s.cfg.Listen),
		zap.Bool("self_signed", selfSigned),
		zap.String("cert_sha256", hex.EncodeToString(fp[:])),
	)
	go func() {
		// Cert is already in TLSConfig, so file args are empty.
		if err := s.srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("HTTPS server stopped", zap.Error(err))
		}
	}()
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), s.token) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="plusclouds-agent"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "agent_uuid": s.agentUUID})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var env protocol.Envelope
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid envelope: " + err.Error()})
		return
	}
	// Dispatch always yields a result envelope (completed/failed/rejected), so
	// the HTTP status is 200 and the outcome lives in payload.status. 202 means
	// the command was accepted as a background job (params.async).
	result := s.disp.Dispatch(r.Context(), env)
	code := http.StatusOK
	var rp protocol.ResultPayload
	if json.Unmarshal(result.Payload, &rp) == nil && rp.Status == protocol.StatusAccepted {
		code = http.StatusAccepted // long operation started in the background
	}
	writeJSON(w, code, result)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// MinTokenLength is the shortest bearer token the HTTPS server accepts.
const MinTokenLength = 32

// CheckToken refuses placeholder and short tokens so a sample config copied
// without editing cannot expose the API.
func CheckToken(token string) error {
	if strings.HasPrefix(strings.ToUpper(token), "CHANGE_ME") {
		return errors.New("http api_key is still the placeholder; set a long random token")
	}
	if len(token) < MinTokenLength {
		return fmt.Errorf("http api_key is too short (%d chars); use at least %d random characters", len(token), MinTokenLength)
	}
	return nil
}
