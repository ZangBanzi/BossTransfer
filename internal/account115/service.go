package account115

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"bosstransfer/internal/adapters/open115"
)

type Gateway interface {
	QRImageURL(string) string
	StartDeviceAuth(context.Context, string, string) (open115.DeviceCode, error)
	QRStatus(context.Context, string, int64, string) (open115.QRStatus, error)
	ExchangeDeviceCode(context.Context, string, string) (open115.Tokens, error)
	Refresh(context.Context, string) (open115.Tokens, error)
	UserInfo(context.Context, string) (open115.UserInfo, error)
	Search(context.Context, string, string, string, int) ([]open115.File, error)
	ListDirectories(context.Context, string, string) ([]open115.Directory, error)
	GetFolderInfo(context.Context, string, string) (open115.FolderInfo, error)
	DownloadURL(context.Context, string, string) (open115.Download, error)
	UploadInit(context.Context, string, open115.UploadInitRequest) (open115.UploadInitResponse, error)
}

type AuthState struct {
	Active     bool   `json:"active"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	QRCode     string `json:"qrcode,omitempty"`
	QRImageURL string `json:"qr_image_url,omitempty"`
	Bound      bool   `json:"bound"`
}

type authSession struct {
	code      open115.DeviceCode
	verifier  string
	startedAt time.Time
}

type Service struct {
	store   *Store
	api     Gateway
	now     func() time.Time
	mu      sync.Mutex
	auth    *authSession
	refresh sync.Mutex
}

func NewService(store *Store, api Gateway) *Service {
	if api == nil {
		api = open115.New(nil)
	}
	return &Service{store: store, api: api, now: time.Now}
}

func (s *Service) Public() PublicConfig { return s.store.Public() }

func (s *Service) SetClientID(clientID string) error { return s.store.SetClientID(clientID) }

func (s *Service) StartAuth(ctx context.Context, clientID string) (AuthState, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID != "" {
		if err := s.store.SetClientID(clientID); err != nil {
			return AuthState{}, err
		}
	}
	cfg := s.store.Get()
	if cfg.ClientID == "" {
		return AuthState{}, errors.New("请先填写 115 Open AppID")
	}
	verifier, err := randomVerifier()
	if err != nil {
		return AuthState{}, err
	}
	code, err := s.api.StartDeviceAuth(ctx, cfg.ClientID, verifier)
	if err != nil {
		return AuthState{}, err
	}
	if code.UID == "" || code.Sign == "" {
		return AuthState{}, errors.New("115 未返回完整授权二维码")
	}
	s.mu.Lock()
	s.auth = &authSession{code: code, verifier: verifier, startedAt: s.now().UTC()}
	s.mu.Unlock()
	return AuthState{Active: true, Status: "waiting", Message: "请使用 115 手机客户端扫码并确认", QRCode: code.QRCode, QRImageURL: s.api.QRImageURL(code.UID)}, nil
}

func (s *Service) PollAuth(ctx context.Context) (AuthState, error) {
	s.mu.Lock()
	if s.auth == nil {
		s.mu.Unlock()
		return AuthState{Status: "idle", Bound: s.store.Public().Bound}, nil
	}
	session := *s.auth
	s.mu.Unlock()
	if s.now().UTC().Sub(session.startedAt) > 10*time.Minute {
		s.clearAuth()
		return AuthState{Status: "expired", Message: "二维码已过期，请重新获取"}, nil
	}
	status, err := s.api.QRStatus(ctx, session.code.UID, session.code.Time, session.code.Sign)
	if err != nil {
		return AuthState{}, err
	}
	result := AuthState{Active: true, Status: "waiting", Message: status.Message, QRCode: session.code.QRCode, QRImageURL: s.api.QRImageURL(session.code.UID)}
	switch status.Status {
	case 0:
		result.Status = "waiting"
	case 1:
		result.Status = "scanned"
		if result.Message == "" {
			result.Message = "已扫码，请在 115 客户端确认"
		}
	case 2:
		tokens, exchangeErr := s.api.ExchangeDeviceCode(ctx, session.code.UID, session.verifier)
		if exchangeErr != nil {
			return AuthState{}, exchangeErr
		}
		user, userErr := s.api.UserInfo(ctx, tokens.AccessToken)
		if userErr != nil {
			return AuthState{}, userErr
		}
		expires := s.now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second)
		if tokens.ExpiresIn <= 0 {
			expires = s.now().UTC().Add(2 * time.Hour)
		}
		if err := s.store.SaveAccount(tokens.AccessToken, tokens.RefreshToken, expires, user.UserID, user.UserName, user.Avatar); err != nil {
			return AuthState{}, err
		}
		s.clearAuth()
		return AuthState{Status: "bound", Message: "115 账号绑定成功", Bound: true}, nil
	case -1, -2:
		s.clearAuth()
		return AuthState{Status: "expired", Message: firstNonEmpty(status.Message, "二维码已失效，请重新获取")}, nil
	default:
		result.Status = "waiting"
	}
	return result, nil
}

func (s *Service) ClearAccount() error {
	s.clearAuth()
	return s.store.ClearAccount()
}

func (s *Service) SelectFolder(ctx context.Context, id, name string) error {
	id = strings.TrimSpace(id)
	if id == "" || id == "0" {
		return s.store.SelectFolder("0", "根目录")
	}
	info, err := callWithToken(s, ctx, func(token string) (open115.FolderInfo, error) {
		return s.api.GetFolderInfo(ctx, token, id)
	})
	if err != nil {
		return err
	}
	if info.FileID != "" && info.FileID != id {
		return errors.New("115 目录编号不匹配")
	}
	if strings.TrimSpace(info.FileName) != "" {
		name = info.FileName
	}
	return s.store.SelectFolder(id, name)
}

func (s *Service) Search(ctx context.Context, query, cid string, limit int) ([]open115.File, error) {
	return callWithToken(s, ctx, func(token string) ([]open115.File, error) {
		return s.api.Search(ctx, token, query, cid, limit)
	})
}

func (s *Service) ListDirectories(ctx context.Context, cid string) ([]open115.Directory, error) {
	return callWithToken(s, ctx, func(token string) ([]open115.Directory, error) {
		return s.api.ListDirectories(ctx, token, cid)
	})
}

func (s *Service) FolderInfo(ctx context.Context, id string) (open115.FolderInfo, error) {
	return callWithToken(s, ctx, func(token string) (open115.FolderInfo, error) {
		return s.api.GetFolderInfo(ctx, token, id)
	})
}

func (s *Service) DownloadURL(ctx context.Context, pickCode string) (open115.Download, error) {
	return callWithToken(s, ctx, func(token string) (open115.Download, error) {
		return s.api.DownloadURL(ctx, token, pickCode)
	})
}

func (s *Service) UploadInit(ctx context.Context, request open115.UploadInitRequest) (open115.UploadInitResponse, error) {
	if strings.TrimSpace(request.Target) == "" {
		request.Target = s.store.Get().FolderID
	}
	return callWithToken(s, ctx, func(token string) (open115.UploadInitResponse, error) {
		return s.api.UploadInit(ctx, token, request)
	})
}

func callWithToken[T any](s *Service, ctx context.Context, call func(string) (T, error)) (T, error) {
	var zero T
	token, err := s.accessToken(ctx, false)
	if err != nil {
		return zero, err
	}
	result, err := call(token)
	if !open115.IsAuthorizationError(err) {
		return result, err
	}
	token, refreshErr := s.accessToken(ctx, true)
	if refreshErr != nil {
		return zero, refreshErr
	}
	return call(token)
}

func (s *Service) accessToken(ctx context.Context, force bool) (string, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()
	cfg := s.store.Get()
	if cfg.AccessToken == "" || cfg.RefreshToken == "" {
		return "", errors.New("请先绑定 115 账号")
	}
	if !force && (cfg.ExpiresAt.IsZero() || s.now().UTC().Add(5*time.Minute).Before(cfg.ExpiresAt)) {
		return cfg.AccessToken, nil
	}
	tokens, err := s.api.Refresh(ctx, cfg.RefreshToken)
	if err != nil {
		return "", err
	}
	expires := s.now().UTC().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.ExpiresIn <= 0 {
		expires = s.now().UTC().Add(2 * time.Hour)
	}
	if err := s.store.SaveAccount(tokens.AccessToken, tokens.RefreshToken, expires, cfg.UserID, cfg.UserName, cfg.Avatar); err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func (s *Service) clearAuth() {
	s.mu.Lock()
	s.auth = nil
	s.mu.Unlock()
}

func randomVerifier() (string, error) {
	value := make([]byte, 64)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
