package api

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"sync-center/core"
	"sync-center/httpx"
)

const (
	sessionCookie = "session"
	// authReadTimeout 限制读取登录请求体的时间，半开连接不能长期占用登录槽位。
	authReadTimeout = 15 * time.Second
)

type authRequest struct {
	Token string `json:"token"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	select {
	case s.authGate <- struct{}{}:
	case <-r.Context().Done():
		return
	case <-time.After(gateWait):
		s.fail(w, r, http.StatusServiceUnavailable, "busy", "error.busy")
		return
	}

	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(authReadTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var request authRequest
	decodeErr := json.NewDecoder(r.Body).Decode(&request)
	valid := decodeErr == nil && request.Token != "" && core.VerifySecret(s.app.Config.AuthHash, request.Token)
	<-s.authGate
	_ = controller.SetReadDeadline(time.Time{})

	// 恒定耗时的等待放在槽位之外，登录失败不会拖住其他登录
	s.waitVerification(started)
	if decodeErr != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid_body", "error.invalid-body")
		return
	}
	if !valid {
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "error.invalid-token")
		return
	}

	token, err := s.app.Sessions.Issue()
	if err != nil {
		s.logger.Error("issue session", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secureRequest(r),
	})
	httpx.JSON(w, http.StatusOK, map[string]string{"token": token})
}

// waitVerification 让失败与成功的耗时都不低于一秒，避免用响应时间区分口令。
func (s *Server) waitVerification(started time.Time) {
	minimum := time.Second + time.Duration(rand.Int64N(int64(200*time.Millisecond)))
	if elapsed := time.Since(started); elapsed < minimum {
		time.Sleep(minimum - elapsed)
	}
}

func (s *Server) secureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) sessionToken(r *http.Request) string {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

func (s *Server) authenticated(r *http.Request) bool {
	token := s.sessionToken(r)
	return token != "" && s.app.Sessions.Valid(token)
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sync-center"`)
			s.fail(w, r, http.StatusUnauthorized, "unauthorized", "error.auth-required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
