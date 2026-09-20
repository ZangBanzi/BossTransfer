package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"bosstransfer/internal/httpapi"
)

func requireSetupAuth(username, password string, next http.Handler) http.Handler {
	if strings.TrimSpace(password) == "" {
		return next
	}
	wantUser := sha256.Sum256([]byte(username))
	wantPassword := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, ok := r.BasicAuth()
		gotUserHash := sha256.Sum256([]byte(gotUser))
		gotPasswordHash := sha256.Sum256([]byte(gotPassword))
		valid := subtle.ConstantTimeCompare(gotUserHash[:], wantUser[:]) & subtle.ConstantTimeCompare(gotPasswordHash[:], wantPassword[:])
		if !ok || valid != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="BossTransfer storage setup", charset="UTF-8"`)
			w.Header().Set("Cache-Control", "no-store")
			if strings.HasPrefix(r.URL.Path, "/api/") {
				httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required", "message": "需要后端接入账号和密码"})
				return
			}
			http.Error(w, "需要后端接入账号和密码", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireSafeMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutationMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			httpapi.WriteJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "json_required", "message": "请求必须使用 application/json"})
			return
		}
		if !requestOriginAllowed(r) {
			httpapi.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cross_site_request_blocked", "message": "已拒绝跨站设置请求"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func requestOriginAllowed(r *http.Request) bool {
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site == "cross-site" {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
