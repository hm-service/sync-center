package api

import (
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"sync-center/app"
	"sync-center/core"
	"sync-center/httpx"
)

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

type Server struct {
	app        *app.App
	mux        *http.ServeMux
	static     fs.FS
	logger     *slog.Logger
	apiGate    chan struct{}
	staticGate chan struct{}
}

type principal struct {
	core.Principal
	NamespaceID int64
}

func New(application *app.App, static fs.FS, logger *slog.Logger) *Server {
	server := &Server{
		app:        application,
		mux:        http.NewServeMux(),
		static:     static,
		logger:     logger,
		apiGate:    make(chan struct{}, application.Config.MaxAPI),
		staticGate: make(chan struct{}, application.Config.MaxStatic),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.recover(s.log(s.secure(s.mux)))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /livez", s.livez)
	s.mux.HandleFunc("/livez", s.methodNotAllowed("GET, HEAD"))

	s.mux.HandleFunc("GET /readyz", s.readyz)
	s.mux.HandleFunc("/readyz", s.methodNotAllowed("GET, HEAD"))

	// 页面与静态资源共用一个兜底，避免 "GET /" 与 "/assets/" 的匹配度互相冲突
	s.mux.Handle("/", s.gate(s.staticGate, http.HandlerFunc(s.site)))

	s.mux.Handle("GET /ui/api/namespaces", s.gate(s.apiGate, http.HandlerFunc(s.uiNamespaces)))
	s.mux.Handle("GET /ui/api/namespaces/{name}/snapshots", s.gate(s.apiGate, http.HandlerFunc(s.uiSnapshots)))
	s.mux.Handle("GET /ui/api/namespaces/{name}/snapshots/{seq}", s.gate(s.apiGate, http.HandlerFunc(s.uiSnapshot)))
	s.mux.Handle("GET /ui/api/blob/{digest}", s.gate(s.apiGate, http.HandlerFunc(s.uiBlob)))

	s.mux.Handle("POST /v1/lease", s.gate(s.apiGate, http.HandlerFunc(s.createLease)))
	s.mux.Handle("/v1/lease", s.methodNotAllowed("POST"))

	s.mux.Handle("POST /v1/lease/{id}/heartbeat", s.gate(s.apiGate, http.HandlerFunc(s.heartbeat)))
	s.mux.Handle("/v1/lease/{id}/heartbeat", s.methodNotAllowed("POST"))

	s.mux.Handle("DELETE /v1/lease/{id}", s.gate(s.apiGate, http.HandlerFunc(s.releaseLease)))
	s.mux.Handle("/v1/lease/{id}", s.methodNotAllowed("DELETE"))

	s.mux.Handle("PUT /v1/blobs/{hash}", s.gate(s.apiGate, http.HandlerFunc(s.putBlob)))
	s.mux.Handle("HEAD /v1/blobs/{hash}", s.gate(s.apiGate, http.HandlerFunc(s.headBlob)))
	s.mux.Handle("GET /v1/blobs/{hash}", s.gate(s.apiGate, http.HandlerFunc(s.getBlob)))
	s.mux.Handle("/v1/blobs/{hash}", s.methodNotAllowed("GET, HEAD, PUT"))

	s.mux.Handle("POST /v1/snapshots", s.gate(s.apiGate, http.HandlerFunc(s.createSnapshot)))
	s.mux.Handle("GET /v1/snapshots", s.gate(s.apiGate, http.HandlerFunc(s.listSnapshots)))
	s.mux.Handle("/v1/snapshots", s.methodNotAllowed("GET, HEAD, POST"))

	s.mux.Handle("GET /v1/snapshots/{seq}", s.gate(s.apiGate, http.HandlerFunc(s.getSnapshot)))
	s.mux.Handle("/v1/snapshots/{seq}", s.methodNotAllowed("GET, HEAD"))

	s.mux.Handle("GET /v1/metadata", s.gate(s.apiGate, http.HandlerFunc(s.metadata)))
	s.mux.Handle("/v1/metadata", s.methodNotAllowed("GET, HEAD"))

	s.mux.HandleFunc("/v1/", s.notFound)
}

func (s *Server) livez(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.app.Config.ReadyToken != "" && r.URL.Query().Get("token") != s.app.Config.ReadyToken {
		httpx.Fail(w, http.StatusUnauthorized, "unauthorized", "invalid readyz token")
		return
	}
	if err := s.app.DB.Probe(); err != nil {
		s.logger.Error("readyz probe failed", "error", err)
		httpx.Fail(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) site(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/ui":
		if !allowRead(r.Method) {
			s.methodNotAllowed("GET, HEAD")(w, r)
			return
		}
		s.page(w, r)
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		if !allowRead(r.Method) {
			s.methodNotAllowed("GET, HEAD")(w, r)
			return
		}
		http.StripPrefix("/assets/", http.FileServer(http.FS(s.static))).ServeHTTP(w, r)
	default:
		s.notFound(w, r)
	}
}

func allowRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	index, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		s.logger.Error("read index", "error", err)
		httpx.Fail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

func (s *Server) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		httpx.Fail(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, http.StatusNotFound, "not_found", "not found")
}

func (s *Server) gate(sem chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sem <- struct{}{}
		defer func() { <-sem }()
		next.ServeHTTP(w, r)
	})
}

// principal 从 Bearer token 解出 namespace；token 的权限在配置里，运行期只留摘要。
func (s *Server) principal(r *http.Request) (principal, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return principal{}, errUnauthorized
	}
	found, ok := s.app.Tokens.Lookup(strings.TrimPrefix(header, "Bearer "))
	if !ok {
		return principal{}, errUnauthorized
	}
	namespaceID, ok := s.app.NamespaceIDs[found.Namespace]
	if !ok {
		return principal{}, errUnauthorized
	}
	return principal{Principal: found, NamespaceID: namespaceID}, nil
}

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

func (s *Server) require(w http.ResponseWriter, r *http.Request, write bool) (principal, bool) {
	p, err := s.principal(r)
	if err != nil {
		httpx.Fail(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
		return principal{}, false
	}
	if write && !p.CanWrite {
		httpx.Fail(w, http.StatusForbidden, "forbidden", "write permission required")
		return principal{}, false
	}
	return p, true
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

func (s *Server) log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/livez" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"client_ip", host,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("panic", "path", r.URL.Path, "value", value)
				httpx.Fail(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
