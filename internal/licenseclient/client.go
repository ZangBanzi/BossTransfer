package licenseclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Service struct {
	store *Store
	http  *http.Client
	now   func() time.Time
	mu    sync.Mutex
}

type remoteResponse struct {
	DeviceID       string    `json:"device_id"`
	DeviceToken    string    `json:"device_token,omitempty"`
	License        License   `json:"license"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

func NewService(store *Store, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Service{store: store, http: client, now: time.Now}
}

func (s *Service) Status() Status { return s.store.Status(s.now()) }

func (s *Service) Activate(ctx context.Context, managerURL, code, deviceLabel string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	managerURL = strings.TrimRight(strings.TrimSpace(managerURL), "/")
	if err := validateManagerURL(managerURL); err != nil {
		return s.Status(), err
	}
	if strings.TrimSpace(code) == "" {
		return s.Status(), errors.New("请输入授权码")
	}
	_, installationID, publicKey := s.store.Identity()
	payload := map[string]string{"code": strings.TrimSpace(code), "installation_id": installationID, "public_key": publicKey, "device_label": strings.TrimSpace(deviceLabel)}
	var response remoteResponse
	if err := s.post(ctx, managerURL+"/api/v1/licensing/activate", payload, "", &response); err != nil {
		return s.Status(), err
	}
	if response.DeviceToken == "" {
		return s.Status(), errors.New("授权服务器未返回设备凭据")
	}
	now := s.now().UTC()
	if err := s.store.SaveActivation(managerURL, response.DeviceID, response.DeviceToken, response.License, response.LeaseExpiresAt, now); err != nil {
		return s.Status(), err
	}
	return s.Status(), nil
}

func (s *Service) Heartbeat(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	managerURL, _, _ := s.store.Identity()
	deviceID, token, err := s.store.DeviceCredential()
	if err != nil {
		return s.Status(), err
	}
	var response remoteResponse
	err = s.post(ctx, strings.TrimRight(managerURL, "/")+"/api/v1/licensing/heartbeat", map[string]string{"device_id": deviceID}, token, &response)
	now := s.now().UTC()
	if err != nil {
		var remoteErr RemoteError
		if errors.As(err, &remoteErr) && (remoteErr.StatusCode == http.StatusForbidden || remoteErr.StatusCode == http.StatusUnauthorized) {
			_ = s.store.MarkRemoteStatus(remoteErr.Code, remoteErr.Message, now)
		}
		return s.Status(), err
	}
	if err := s.store.SaveHeartbeat(response.License, response.LeaseExpiresAt, now); err != nil {
		return s.Status(), err
	}
	return s.Status(), nil
}

type RemoteError struct {
	StatusCode    int
	Code, Message string
}

func (e RemoteError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("授权服务器返回 HTTP %d", e.StatusCode)
}

func (s *Service) post(ctx context.Context, endpoint string, payload any, bearer string, target any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接授权服务器: %w", err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = decoder.Decode(&body)
		return RemoteError{StatusCode: resp.StatusCode, Code: body.Error, Message: body.Message}
	}
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("授权服务器响应无效: %w", err)
	}
	return nil
}

func validateManagerURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("请输入完整的授权服务器 http 或 https 地址")
	}
	return nil
}
