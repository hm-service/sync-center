package api

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sync-center/app"
	"sync-center/core"
	"sync-center/httpx"
	"sync-center/i18n"
)

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// gateWait 限制请求在并发闸门前排队的时间，避免半开连接无限占位。
// authSlots 是登录的独立预算：Argon2 每次约占 19 MiB，且恒定耗时 1 秒。
const (
	gateWait  = 10 * time.Second
	authSlots = 2
)

type Server struct {
	app        *app.App
	mux        *http.ServeMux
	static     fs.FS
	logger     *slog.Logger
	apiGate    chan struct{}
	authGate   chan struct{}
	staticGate chan struct{}
}

func New(application *app.App, static fs.FS, logger *slog.Logger) *Server {
	server := &Server{
		app:        application,
		mux:        http.NewServeMux(),
		static:     static,
		logger:     logger,
		apiGate:    make(chan struct{}, application.Config.MaxAPI),
		authGate:   make(chan struct{}, authSlots),
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

	// 登录单独限流：既不能挤占 /v1 的配额，也不能让 Argon2 并发堆爆内存
	s.mux.Handle("POST /api/auth", s.gate(s.authGate, http.HandlerFunc(s.login)))
	s.mux.Handle("/api/auth", s.methodNotAllowed("POST"))

	s.mux.Handle("GET /admin/api/namespaces", s.protected(s.adminNamespaces))
	s.mux.Handle("GET /admin/api/namespaces/{name}/snapshots", s.protected(s.adminSnapshots))
	s.mux.Handle("GET /admin/api/namespaces/{name}/snapshots/{seq}", s.protected(s.adminSnapshot))
	// 下载不参与清理暂停：unlink 之后已打开的文件仍可读完
	s.mux.Handle("GET /admin/api/blob/{digest}", s.gate(s.apiGate, s.requireSession(http.HandlerFunc(s.adminBlob))))

	// 运维接口：回收是全局的，只认 GC_TOKEN，所以自己不能被暂停锁包住（TryLock 会拿不到写锁）。
	// GC_TOKEN 为空时不注册，路径落到 404。
	if s.app.Config.GCEnabled() {
		s.mux.Handle("POST /admin/api/gc", s.gate(s.apiGate, http.HandlerFunc(s.collectGarbage)))
		s.mux.Handle("/admin/api/gc", s.methodNotAllowed("POST"))
	}

	s.mux.Handle("POST /v1/lease", s.guarded(s.apiGate, http.HandlerFunc(s.createLease)))
	s.mux.Handle("/v1/lease", s.methodNotAllowed("POST"))

	s.mux.Handle("POST /v1/lease/{id}/heartbeat", s.guarded(s.apiGate, http.HandlerFunc(s.heartbeat)))
	s.mux.Handle("/v1/lease/{id}/heartbeat", s.methodNotAllowed("POST"))

	s.mux.Handle("DELETE /v1/lease/{id}", s.guarded(s.apiGate, http.HandlerFunc(s.releaseLease)))
	s.mux.Handle("/v1/lease/{id}", s.methodNotAllowed("DELETE"))

	s.mux.Handle("PUT /v1/blobs/{hash}", s.gate(s.apiGate, http.HandlerFunc(s.putBlob)))
	s.mux.Handle("HEAD /v1/blobs/{hash}", s.gate(s.apiGate, http.HandlerFunc(s.headBlob)))
	s.mux.Handle("GET /v1/blobs/{hash}", s.gate(s.apiGate, http.HandlerFunc(s.getBlob)))
	s.mux.Handle("/v1/blobs/{hash}", s.methodNotAllowed("GET, HEAD, PUT"))

	s.mux.Handle("POST /v1/snapshots", s.guarded(s.apiGate, http.HandlerFunc(s.createSnapshot)))
	s.mux.Handle("GET /v1/snapshots", s.guarded(s.apiGate, http.HandlerFunc(s.listSnapshots)))
	s.mux.Handle("/v1/snapshots", s.methodNotAllowed("GET, HEAD, POST"))

	s.mux.Handle("GET /v1/snapshots/{seq}", s.guarded(s.apiGate, http.HandlerFunc(s.getSnapshot)))
	s.mux.Handle("/v1/snapshots/{seq}", s.methodNotAllowed("GET, HEAD"))

	s.mux.Handle("GET /v1/metadata", s.guarded(s.apiGate, http.HandlerFunc(s.metadata)))
	s.mux.Handle("/v1/metadata", s.methodNotAllowed("GET, HEAD"))

	s.mux.HandleFunc("/v1/", s.notFound)
}

func (s *Server) livez(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.app.Config.ReadyToken != "" && r.URL.Query().Get("token") != s.app.Config.ReadyToken {
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "error.readyz-token")
		return
	}
	if err := s.app.Snapshots.Probe(); err != nil {
		s.logger.Error("readyz probe failed", "error", err)
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "error.unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) site(w http.ResponseWriter, r *http.Request) {
	switch {
	// 单页应用：/auth 与 /admin 返回同一份入口，由前端按路径决定渲染哪个视图
	case r.URL.Path == "/auth" || r.URL.Path == "/" || r.URL.Path == "/admin" || r.URL.Path == "/admin/":
		if !allowRead(r.Method) {
			s.methodNotAllowed("GET, HEAD")(w, r)
			return
		}
		if r.URL.Path != "/auth" && !s.authenticated(r) {
			http.Redirect(w, r, "/auth", http.StatusFound)
			return
		}
		s.page(w, r, "index.html")
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		if !allowRead(r.Method) {
			s.methodNotAllowed("GET, HEAD")(w, r)
			return
		}
		// Vite 的产物在 dist/assets/ 下，前缀与目录同名，不需要 StripPrefix
		http.FileServer(http.FS(s.static)).ServeHTTP(w, r)
	default:
		s.notFound(w, r)
	}
}

func allowRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, name string) {
	index, err := fs.ReadFile(s.static, name)
	if err != nil {
		s.logger.Error("read index", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

// fail 输出本地化后的错误；错误码保持英文，供客户端做分支判断。
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, key string, args ...any) {
	httpx.Fail(w, status, code, i18n.T(language(r), key, args...))
}

func language(r *http.Request) string {
	return i18n.Parse(r.Header.Get("Accept-Language"))
}

func (s *Server) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		s.fail(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "error.method-not-allowed")
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "not_found", "error.not-found")
}

func (s *Server) protected(handler http.HandlerFunc) http.Handler {
	return s.guarded(s.apiGate, s.requireSession(handler))
}

// guarded 在并发闸门之后再加一道暂停读锁：清理期间这些请求排队，通常只有毫秒级。
// blob 传输与登录不参与，前者耗时长且不会被清理影响，后者恒定耗时 1 秒。
func (s *Server) guarded(sem chan struct{}, next http.Handler) http.Handler {
	return s.gate(sem, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.app.Guard.RLock()
		defer s.app.Guard.RUnlock()
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) gate(sem chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		case <-r.Context().Done():
			return
		case <-time.After(gateWait):
			s.fail(w, r, http.StatusServiceUnavailable, "busy", "error.busy")
		}
	})
}

// principal 从 Bearer token 解出 namespace；token 的权限在配置里，运行期只留摘要。
func (s *Server) principal(r *http.Request) (core.Principal, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return core.Principal{}, errUnauthorized
	}
	found, ok := s.app.Tokens.Lookup(strings.TrimPrefix(header, "Bearer "))
	if !ok {
		return core.Principal{}, errUnauthorized
	}
	return found, nil
}

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

func (s *Server) require(w http.ResponseWriter, r *http.Request, write bool) (core.Principal, bool) {
	p, err := s.principal(r)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "error.unauthorized")
		return core.Principal{}, false
	}
	if write && !p.CanWrite {
		s.fail(w, r, http.StatusForbidden, "forbidden", "error.forbidden")
		return core.Principal{}, false
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

// Unwrap 让 http.ResponseController 能拿到真正的 ResponseWriter（读超时、Flush）。
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// ReadFrom 转发给底层，让 ServeFile 走 sendfile 而不是用户态拷贝。
func (r *recorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

func (r *recorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
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
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"client_ip", httpx.ClientIP(r, s.app.Config.TrustedProxies),
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
		w.Header().Set("Content-Language", language(r))
		w.Header().Add("Vary", "Accept-Language")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("panic", "path", r.URL.Path, "value", value)
				s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
