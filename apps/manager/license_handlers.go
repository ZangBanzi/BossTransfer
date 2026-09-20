package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/licensing"
)

type licenseAPI struct{ store *licensing.Store }

func (a licenseAPI) publicActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	var p licensing.ActivateRequest
	if err := decodeManagerJSON(w, r, &p); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := a.store.Activate(p)
	if err != nil {
		writeLicensingError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}
func (a licenseAPI) publicHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	var p struct {
		DeviceID string `json:"device_id"`
	}
	if err := decodeManagerJSON(w, r, &p); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := a.store.Heartbeat(p.DeviceID, r.Header.Get("Authorization"))
	if err != nil {
		writeLicensingError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (a licenseAPI) licenses(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"licenses": a.store.ListLicenses()})
	case http.MethodPost:
		var p licensing.CreateLicenseRequest
		if err := decodeManagerJSON(w, r, &p); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		result, err := a.store.CreateLicense(p)
		if err != nil {
			writeLicensingError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusCreated, result)
	default:
		allow(w, "GET, POST")
	}
}

func (a licenseAPI) licenseAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/manager/licenses/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodPut {
		var payload licensing.UpdateLicenseRequest
		if err := decodeManagerJSON(w, r, &payload); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		result, err := a.store.UpdateLicense(parts[0], payload)
		if err != nil {
			writeLicensingError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"license": result})
		return
	}
	if len(parts) != 2 || parts[0] == "" {
		writeAPIError(w, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	switch parts[1] {
	case "codes":
		result, err := a.store.IssueCode(parts[0])
		if err != nil {
			writeLicensingError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusCreated, result)
	case "revoke":
		result, err := a.store.RevokeLicense(parts[0])
		if err != nil {
			writeLicensingError(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"license": result})
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "接口不存在")
	}
}

func (a licenseAPI) deviceAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/manager/devices/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "release" {
		writeAPIError(w, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	if r.Method != http.MethodPost {
		allow(w, http.MethodPost)
		return
	}
	device, err := a.store.ReleaseDevice(parts[0])
	if err != nil {
		writeLicensingError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"device": device})
}

func decodeManagerJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func allow(w http.ResponseWriter, value string) {
	w.Header().Set("Allow", value)
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不支持")
}
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	httpapi.WriteJSON(w, status, map[string]string{"error": code, "message": message})
}
func writeLicensingError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "licensing_failed", "授权操作失败"
	switch {
	case errors.Is(err, licensing.ErrInvalidInput):
		status, code, message = http.StatusUnprocessableEntity, "invalid_input", "授权信息不完整或格式无效"
	case errors.Is(err, licensing.ErrLicenseNotFound):
		status, code, message = http.StatusNotFound, "license_not_found", "没有找到这条授权"
	case errors.Is(err, licensing.ErrAuthorizationCode):
		status, code, message = http.StatusUnauthorized, "authorization_code_invalid", "授权码无效"
	case errors.Is(err, licensing.ErrAuthorizationCodeUsed):
		status, code, message = http.StatusConflict, "authorization_code_used", "授权码已由其他设备使用"
	case errors.Is(err, licensing.ErrDeviceLimitReached):
		status, code, message = http.StatusConflict, "device_limit_reached", "已达到允许激活的设备数量"
	case errors.Is(err, licensing.ErrLicenseRevoked):
		status, code, message = http.StatusForbidden, "revoked", "授权已被管理员撤销"
	case errors.Is(err, licensing.ErrLicenseExpired):
		status, code, message = http.StatusForbidden, "expired", "授权已到期"
	case errors.Is(err, licensing.ErrDeviceToken):
		status, code, message = http.StatusUnauthorized, "device_token_invalid", "设备凭据无效"
	case errors.Is(err, licensing.ErrDeviceReleased), errors.Is(err, licensing.ErrDeviceRevoked):
		status, code, message = http.StatusForbidden, "revoked", "此设备已被管理员释放或撤销"
	case errors.Is(err, licensing.ErrDeviceNotFound):
		status, code, message = http.StatusNotFound, "device_not_found", "没有找到此设备"
	case errors.Is(err, licensing.ErrInstallationConflict):
		status, code, message = http.StatusConflict, "installation_conflict", "设备身份发生冲突，请联系管理员"
	}
	writeAPIError(w, status, code, message)
}
