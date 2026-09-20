package main

import (
	"context"
	"net/http"
	"strings"

	"bosstransfer/internal/account115"
	"bosstransfer/internal/httpapi"
)

type appConfigGateway interface {
	AppConfig(context.Context) (string, error)
}

type client115API struct {
	account *account115.Service
	catalog appConfigGateway
}

func (a client115API) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"account": a.account.Public()})
}

func (a client115API) startAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	clientID, err := a.catalog.AppConfig(r.Context())
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "app_config_failed", "message": err.Error()})
		return
	}
	state, err := a.account.StartAuth(r.Context(), clientID)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "auth_start_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"auth": state, "account": a.account.Public()})
}

func (a client115API) authStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	state, err := a.account.PollAuth(r.Context())
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "auth_status_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"auth": state, "account": a.account.Public()})
}

func (a client115API) directories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	if cid == "" {
		cid = "0"
	}
	directories, err := a.account.ListDirectories(r.Context(), cid)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "directories_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"cid": cid, "directories": directories})
}

func (a client115API) selectFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var payload struct {
		FolderID   string `json:"folder_id"`
		FolderName string `json:"folder_name"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	if err := a.account.SelectFolder(r.Context(), payload.FolderID, payload.FolderName); err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_folder", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"account": a.account.Public()})
}

func (a client115API) accountAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if err := a.account.ClearAccount(); err != nil {
		httpapi.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "unbind_failed", "message": err.Error()})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"account": a.account.Public()})
}
