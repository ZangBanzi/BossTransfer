package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"bosstransfer/internal/buildinfo"
)

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type ReadyResponse struct {
	Status string  `json:"status"`
	Checks []Check `json:"checks"`
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": buildinfo.Version,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func ReadyHandler(checks func(*http.Request) []Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := checks(r)
		status := "ready"
		code := http.StatusOK
		for _, check := range result {
			if check.Status == "fail" {
				status = "not_ready"
				code = http.StatusServiceUnavailable
				break
			}
			if check.Status == "warn" && status == "ready" {
				status = "degraded"
			}
		}
		WriteJSON(w, code, ReadyResponse{Status: status, Checks: result})
	}
}

func Method(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		next(w, r)
	}
}
