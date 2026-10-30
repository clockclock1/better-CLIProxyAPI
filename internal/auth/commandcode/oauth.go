// Package commandcode implements Command Code CLI OAuth and credential helpers.
package commandcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// DefaultStudioBaseURL is the Command Code studio host used for CLI login.
	DefaultStudioBaseURL = "https://commandcode.ai"
	// DefaultAPIBaseURL is the Command Code API host for /alpha/generate.
	DefaultAPIBaseURL = "https://api.commandcode.ai"
	// DefaultCLIVersion matches the desktop-shipped command-code CLI; server minVersion is 0.18.10.
	DefaultCLIVersion = "1.88.1"

	oauthTimeout = 5 * time.Minute
)

// CallbackPayload is returned by the studio/auth/cli redirect or POST callback.
type CallbackPayload struct {
	APIKey   string `json:"apiKey"`
	State    string `json:"state"`
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
	KeyName  string `json:"keyName"`
	Error    string `json:"error,omitempty"`
	ErrorDesc string `json:"error_description,omitempty"`
}

// BuildAuthURL constructs the studio CLI OAuth URL.
func BuildAuthURL(studioBase, callbackURL, state, client string) string {
	base := strings.TrimRight(strings.TrimSpace(studioBase), "/")
	if base == "" {
		base = DefaultStudioBaseURL
	}
	if client == "" {
		client = "cli"
	}
	q := url.Values{}
	q.Set("callback", callbackURL)
	q.Set("state", state)
	q.Set("client", client)
	q.Set("mode", "redirect")
	return base + "/studio/auth/cli?" + q.Encode()
}

// OAuthServer handles local loopback callbacks for Command Code authentication.
type OAuthServer struct {
	server     *http.Server
	listener   net.Listener
	port       int
	expected   string
	resultChan chan *CallbackPayload
	errorChan  chan error
	mu         sync.Mutex
	running    bool
}

// NewOAuthServer creates a local loopback server for Command Code OAuth.
func NewOAuthServer(port int, expectedState string) *OAuthServer {
	return &OAuthServer{
		port:       port,
		expected:   expectedState,
		resultChan: make(chan *CallbackPayload, 1),
		errorChan:  make(chan error, 1),
	}
}

// Start initiates the local HTTP server. Port 0 selects an ephemeral port.
func (s *OAuthServer) Start() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.port, nil
	}

	ln, errLn := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if errLn != nil {
		return 0, fmt.Errorf("commandcode: bind oauth callback: %w", errLn)
	}
	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", s.handleCallback)
	mux.HandleFunc("/callback/complete", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	s.running = true
	go func() {
		if errServe := s.server.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.Debugf("commandcode oauth server error: %v", errServe)
			select {
			case s.errorChan <- errServe:
			default:
			}
		}
	}()
	return s.port, nil
}

// Stop gracefully stops the server.
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	s.running = false
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// WaitForCallbackWithContext waits for the browser callback, context cancel, or timeout.
func (s *OAuthServer) WaitForCallbackWithContext(ctx context.Context, timeout time.Duration) (*CallbackPayload, error) {
	if timeout <= 0 {
		timeout = oauthTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-s.resultChan:
		return res, nil
	case err := <-s.errorChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("commandcode authentication timed out")
	}
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	allowed := map[string]struct{}{
		"http://localhost:3000":           {},
		"https://staging.commandcode.ai": {},
		"https://commandcode.ai":         {},
	}
	cors := "https://commandcode.ai"
	if _, ok := allowed[origin]; ok {
		cors = origin
	}

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", cors)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method == http.MethodPost {
		s.handlePOST(w, r, cors)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	if errStr := strings.TrimSpace(q.Get("error")); errStr != "" {
		desc := strings.TrimSpace(q.Get("error_description"))
		msg := errStr
		if desc != "" {
			msg = desc
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf(loginFailureHTML, html.EscapeString(msg))))
		s.emit(&CallbackPayload{Error: errStr, ErrorDesc: desc})
		return
	}

	payload := &CallbackPayload{
		APIKey:   strings.TrimSpace(q.Get("apiKey")),
		State:    strings.TrimSpace(q.Get("state")),
		UserID:   strings.TrimSpace(q.Get("userId")),
		UserName: strings.TrimSpace(q.Get("userName")),
		KeyName:  strings.TrimSpace(q.Get("keyName")),
	}
	if payload.APIKey == "" || payload.State != s.expected || payload.UserID == "" || payload.UserName == "" || payload.KeyName == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(loginFailureHTML, "Invalid OAuth callback payload")))
		s.emit(&CallbackPayload{Error: "invalid_callback"})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(loginSuccessHTML))
	s.emit(payload)
}

func (s *OAuthServer) handlePOST(w http.ResponseWriter, r *http.Request, cors string) {
	body, errRead := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if errRead != nil {
		w.Header().Set("Access-Control-Allow-Origin", cors)
		http.Error(w, `{"success":false,"error":"read failed"}`, http.StatusBadRequest)
		return
	}
	var payload CallbackPayload
	if errJSON := json.Unmarshal(body, &payload); errJSON != nil {
		w.Header().Set("Access-Control-Allow-Origin", cors)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":"Invalid JSON"}`))
		return
	}
	payload.APIKey = strings.TrimSpace(payload.APIKey)
	payload.State = strings.TrimSpace(payload.State)
	payload.UserID = strings.TrimSpace(payload.UserID)
	payload.UserName = strings.TrimSpace(payload.UserName)
	payload.KeyName = strings.TrimSpace(payload.KeyName)
	if payload.State != s.expected || payload.APIKey == "" {
		w.Header().Set("Access-Control-Allow-Origin", cors)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"error":"Invalid state"}`))
		s.emit(&CallbackPayload{Error: "invalid_state"})
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", cors)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"success":true}`))
	s.emit(&payload)
}

func (s *OAuthServer) emit(payload *CallbackPayload) {
	select {
	case s.resultChan <- payload:
	default:
	}
}

const loginSuccessHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>Authentication Successful</title>
<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#0f172a;color:#e2e8f0}
.card{background:#1e293b;padding:2rem 2.5rem;border-radius:12px;text-align:center;max-width:420px}
h1{margin:0 0 .75rem;font-size:1.4rem;color:#4ade80}p{margin:0;opacity:.85}</style></head>
<body><div class="card"><h1>Login success</h1><p>You can close this window and return to the terminal.</p></div></body></html>`

const loginFailureHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>Authentication Failed</title>
<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#0f172a;color:#e2e8f0}
.card{background:#1e293b;padding:2rem 2.5rem;border-radius:12px;text-align:center;max-width:420px}
h1{margin:0 0 .75rem;font-size:1.4rem;color:#f87171}p{margin:0;opacity:.85}</style></head>
<body><div class="card"><h1>Auth failed</h1><p>%s</p></div></body></html>`
