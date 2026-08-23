// Package httpui is the "temporary localhost" adapter (technical design
// §16.3): a foreground-only HTTP server bound to 127.0.0.1 on a random port,
// serving the embedded static frontend and a single whitelisted invoke
// endpoint. It is not a daemon — it runs only while `mycontext ui serve` is
// running and exits with it.
package httpui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

const tokenHeader = "X-Mycontext-Token"

// Options configure one server instance.
type Options struct {
	Port        int // 0 = random
	IdleTimeout time.Duration
	CLIVersion  string
	Root        string
}

// Server is the localhost adapter. One instance per `ui serve` invocation.
type Server struct {
	opts       Options
	store      *ops.Store
	token      string
	assets     fs.FS
	origin     string // the exact http://127.0.0.1:PORT this instance owns
	lastActive atomic.Int64
	logger     *slog.Logger
}

// New wires a server around an already-open, read-only ops store.
func New(store *ops.Store, assets fs.FS, opts Options) (*Server, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot generate session token")
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Minute
	}
	s := &Server{
		opts:   opts,
		store:  store,
		token:  hex.EncodeToString(tokenBytes),
		assets: assets,
		logger: slog.Default(),
	}
	s.touch()
	return s, nil
}

func (s *Server) touch() { s.lastActive.Store(time.Now().UnixNano()) }

// IdleSince reports how long it has been since the last handled request.
func (s *Server) IdleSince() time.Duration {
	return time.Since(time.Unix(0, s.lastActive.Load()))
}

// Token returns the session token this instance minted. Exported so tests
// can authenticate; production callers get it from the printed URL instead.
func (s *Server) Token() string { return s.token }

// Handler builds the mux on its own, decoupled from listening on a real
// socket, so tests can exercise every route via httptest.NewServer without
// picking a port or racing Serve's goroutine.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/capabilities", s.withGuard(s.handleCapabilities))
	mux.HandleFunc("POST /api/v1/invoke", s.withGuard(s.handleInvoke))
	mux.Handle("/", http.FileServerFS(s.assets))
	return mux
}

// Serve binds a listener and blocks, honouring ctx cancellation and its own
// idle timeout. It never listens on anything but loopback (§16.3: "不监听局域网地址").
func (s *Server) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", "127.0.0.1:"+portArg(s.opts.Port))
	if err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot bind 127.0.0.1")
	}
	addr := listener.Addr().(*net.TCPAddr)
	s.origin = fmt.Sprintf("http://127.0.0.1:%d", addr.Port)

	httpServer := &http.Server{Handler: s.Handler()}

	// Ctrl-C, parent exit or ctx cancellation for any reason all mean the
	// same thing here: this process is not a daemon, so it stops.
	idleTimer := time.NewTicker(1 * time.Minute)
	defer idleTimer.Stop()
	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- httpServer.Serve(listener) }()

	fmt.Printf("mycontext ui listening on %s/?token=%s\n", s.origin, s.token)
	fmt.Println("read-only · foreground · Ctrl-C to stop")

	for {
		select {
		case <-ctx.Done():
			return s.shutdown(httpServer)
		case <-idleTimer.C:
			if s.IdleSince() > s.opts.IdleTimeout {
				fmt.Printf("idle for %s, stopping\n", s.opts.IdleTimeout)
				return s.shutdown(httpServer)
			}
		case err := <-shutdownErr:
			if err != nil && err != http.ErrServerClosed {
				return protocol.Wrap(err, protocol.CodeIntegrity, "server error")
			}
			return nil
		}
	}
}

func (s *Server) shutdown(httpServer *http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func portArg(p int) string {
	if p <= 0 {
		return "0"
	}
	return strconv.Itoa(p)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// withGuard enforces the same-origin + token check described in §16.3
// ("校验 Origin、Host 和 token，拒绝通配 CORS") before an API handler runs.
func (s *Server) withGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()

		if host := r.Header.Get("Host"); host != "" && !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.origin {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		supplied := r.Header.Get(tokenHeader)
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(s.token)) != 1 {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", s.origin)
		next(w, r)
	}
}

func isLoopbackHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "127.0.0.1" || h == "localhost"
}

// capabilities tells the frontend what this build/instance can do, so it can
// hide unsupported actions instead of failing at click-time (§16.2).
type capabilitiesResponse struct {
	Protocol   string   `json:"protocol"`
	Read       bool     `json:"read"`
	Write      bool     `json:"write"`
	Operations []string `json:"operations"`
	Root       string   `json:"root"`
	CLIVersion string   `json:"cli_version"`
}

// queryOperations is the whitelist for this build: read-only, ops.db only.
// There is deliberately no generic SQL or per-table endpoint (§16.1).
var queryOperations = []string{"ops.status", "project.tree"}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, capabilitiesResponse{
		Protocol:   protocol.Version,
		Read:       true,
		Write:      false,
		Operations: queryOperations,
		Root:       s.opts.Root,
		CLIVersion: s.opts.CLIVersion,
	})
}

// invocationRequest mirrors §16.1's shared request shape across Bridge,
// localhost and CLI --input.
type invocationRequest struct {
	Protocol  string          `json:"protocol"`
	Operation string          `json:"operation"`
	RequestID string          `json:"request_id"`
	Actor     string          `json:"actor"`
	Input     json.RawMessage `json:"input"`
}

func (s *Server) handleInvoke(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req invocationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		s.writeEnvelope(w, "", nil, protocol.BadInput("invalid invocation request: %v", err), start)
		return
	}

	ctx := r.Context()
	var data any
	var err error
	switch req.Operation {
	case "ops.status":
		data, err = s.store.Status(ctx)
	case "project.tree":
		data, err = s.store.Tree(ctx, false)
	default:
		err = protocol.BadInput("unsupported operation %q; this build only supports: %s",
			req.Operation, strings.Join(queryOperations, ", "))
	}
	s.writeEnvelope(w, req.Operation, data, err, start)
}

func (s *Server) writeEnvelope(w http.ResponseWriter, command string, data any, err error, start time.Time) {
	env := protocol.Envelope{
		Protocol: protocol.Version,
		Command:  command,
		Meta: protocol.Meta{
			Root:       s.opts.Root,
			CLIVersion: s.opts.CLIVersion,
			DurationMS: time.Since(start).Milliseconds(),
		},
	}
	status := http.StatusOK
	if err != nil {
		app := asAppError(err)
		env.OK = false
		env.Error = &protocol.Error{Code: app.Code, Message: app.Message, Details: app.Details, Retryable: app.Retryable}
		status = httpStatusFor(app.Code)
	} else {
		env.OK = true
		env.Data = data
	}
	writeJSON(w, status, env)
}

func asAppError(err error) *protocol.AppError {
	if app, ok := err.(*protocol.AppError); ok {
		return app
	}
	return &protocol.AppError{Code: protocol.CodeInternal, Message: err.Error()}
}

func httpStatusFor(code string) int {
	switch code {
	case protocol.CodeBadInput:
		return http.StatusBadRequest
	case protocol.CodeNotFound:
		return http.StatusNotFound
	case protocol.CodeForbidden:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
