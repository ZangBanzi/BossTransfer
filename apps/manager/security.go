package main

import (
	"crypto/rand"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bosstransfer/internal/httpapi"
)

func requireSafeMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			httpapi.WriteJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "json_required", "message": "请求必须使用 application/json"})
			return
		}
		if !requestOriginAllowed(r) {
			httpapi.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cross_site_request_blocked", "message": "已拒绝跨站请求"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestOriginAllowed(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")) == "cross-site" {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
}

func loadOrCreatePepper(dataDir, configured string) ([]byte, error) {
	if configured != "" {
		if len(configured) < 16 {
			return nil, errors.New("BOSSTRANSFER_LICENSE_PEPPER 至少需要 16 个字符")
		}
		return []byte(configured), nil
	}
	path := filepath.Join(dataDir, "license-pepper.key")
	value, err := os.ReadFile(path)
	if err == nil {
		if len(value) != 32 {
			return nil, errors.New("授权密钥文件长度无效")
		}
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	value = make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dataDir, ".license-pepper-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(name, path); err != nil {
		return nil, err
	}
	return value, nil
}
