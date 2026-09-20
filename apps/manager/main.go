package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"bosstransfer/internal/account115"
	"bosstransfer/internal/adapters/open115"
	"bosstransfer/internal/buildinfo"
	"bosstransfer/internal/config"
	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/licensing"
	"bosstransfer/internal/localauth"
	"bosstransfer/internal/websession"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		runHealthcheck()
		return
	}

	cfg, err := config.LoadManager()
	if err != nil {
		slog.Error("invalid manager configuration", "error", err)
		os.Exit(2)
	}
	adminStore, err := localauth.Open(filepath.Join(cfg.DataDir, "manager-auth.json"), localauth.Bootstrap{
		Username: cfg.AdminUser, Password: cfg.AdminPassword, MustChange: cfg.AdminUser == config.DefaultManagerUser && cfg.AdminPassword == config.DefaultManagerPassword,
	}, localauth.Options{})
	if err != nil {
		slog.Error("cannot initialize manager web account", "error", err)
		os.Exit(2)
	}
	pepper, err := loadOrCreatePepper(cfg.DataDir, cfg.LicensePepper)
	if err != nil {
		slog.Error("cannot initialize licensing secret", "error", err)
		os.Exit(2)
	}
	licenseStore, err := licensing.NewStore(filepath.Join(cfg.DataDir, "licenses.json"), pepper)
	if err != nil {
		slog.Error("cannot initialize licensing store", "error", err)
		os.Exit(2)
	}
	accountStore, err := account115.Open(filepath.Join(cfg.DataDir, "manager-115.json"), cfg.Open115ClientID)
	if err != nil {
		slog.Error("cannot initialize manager 115 account", "error", err)
		os.Exit(2)
	}
	accountService := account115.NewService(accountStore, open115.New(nil))
	catalog, err := newCatalogAPI(accountService, licenseStore, pepper)
	if err != nil {
		slog.Error("cannot initialize manager catalog", "error", err)
		os.Exit(2)
	}
	webAuth := websession.New(adminStore, "bosstransfer_manager_session", "/login")
	licenses := licenseAPI{store: licenseStore}
	protectAdmin := func(handler http.Handler) http.Handler { return webAuth.RequirePasswordChanged(handler) }
	protectAdminMutation := func(handler http.Handler) http.Handler {
		return webAuth.RequirePasswordChanged(requireSafeMutation(handler))
	}
	protectAuthMutation := func(handler http.Handler) http.Handler {
		return webAuth.Require(requireSafeMutation(handler))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", managerLoginPageHandler(webAuth))
	mux.Handle("/password", webAuth.Require(http.HandlerFunc(managerPasswordPageHandler(webAuth))))
	mux.Handle("/api/v1/manager/auth/login", requireSafeMutation(http.HandlerFunc(webAuth.LoginHandler)))
	mux.Handle("/api/v1/manager/auth/logout", protectAuthMutation(http.HandlerFunc(webAuth.LogoutHandler)))
	mux.HandleFunc("/api/v1/manager/auth/session", webAuth.SessionHandler)
	mux.Handle("/api/v1/manager/auth/credentials", protectAuthMutation(http.HandlerFunc(webAuth.CredentialsHandler)))
	mux.Handle("/api/v1/licensing/activate", requireSafeMutation(http.HandlerFunc(licenses.publicActivate)))
	mux.Handle("/api/v1/licensing/heartbeat", requireSafeMutation(http.HandlerFunc(licenses.publicHeartbeat)))
	mux.Handle("/api/v1/manager/licenses", protectAdminMutation(http.HandlerFunc(licenses.licenses)))
	mux.Handle("/api/v1/manager/licenses/", protectAdminMutation(http.HandlerFunc(licenses.licenseAction)))
	mux.Handle("/api/v1/manager/devices/", protectAdminMutation(http.HandlerFunc(licenses.deviceAction)))
	mux.Handle("/api/v1/manager/catalog", protectAdminMutation(http.HandlerFunc(catalog.managerSettings)))
	mux.Handle("/api/v1/manager/115/auth/start", protectAdminMutation(http.HandlerFunc(catalog.managerAuthStart)))
	mux.Handle("/api/v1/manager/115/auth/status", protectAdmin(http.HandlerFunc(catalog.managerAuthStatus)))
	mux.Handle("/api/v1/manager/115/account", protectAdminMutation(http.HandlerFunc(catalog.managerAccount)))
	mux.Handle("/api/v1/manager/115/directories", protectAdmin(http.HandlerFunc(catalog.managerDirectories)))
	mux.Handle("/api/v1/catalog/app", http.HandlerFunc(catalog.clientApp))
	mux.Handle("/api/v1/catalog/search", http.HandlerFunc(catalog.search))
	mux.Handle("/api/v1/catalog/prepare", requireSafeMutation(http.HandlerFunc(catalog.prepare)))
	mux.Handle("/api/v1/catalog/sign", requireSafeMutation(http.HandlerFunc(catalog.sign)))
	mux.Handle("/api/v1/manager/summary", protectAdmin(httpapi.Method(http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{
			"product":      buildinfo.AppName,
			"version":      buildinfo.Version,
			"role":         "manager",
			"client_port":  envOrDefault("BOSSTRANSFER_CLIENT_PUBLIC_PORT", "18086"),
			"manager_port": envOrDefault("BOSSTRANSFER_MANAGER_PUBLIC_PORT", "18085"),
			"data_dir":     cfg.DataDir,
			"licenses":     len(licenseStore.ListLicenses()),
		})
	})))
	mux.HandleFunc("/api/v1/health/live", httpapi.Method(http.MethodGet, httpapi.LiveHandler()))
	mux.HandleFunc("/api/v1/health/ready", httpapi.Method(http.MethodGet, httpapi.ReadyHandler(func(*http.Request) []httpapi.Check {
		return []httpapi.Check{
			{Name: "process", Status: "ok", Message: "管理端服务运行中"},
			{Name: "licensing", Status: "ok", Message: "中央授权服务运行中"},
			{Name: "storage", Status: "ok", Message: "授权数据存储可用"},
		}
	})))
	mux.Handle("/", protectAdmin(http.HandlerFunc(managerDashboardHandler())))

	server := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("manager starting", "app", buildinfo.AppName, "version", buildinfo.Version, "addr", cfg.Server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("manager stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("manager shutdown failed", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func runHealthcheck() {
	url := os.Getenv("BOSSTRANSFER_HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:8085/api/v1/health/live"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		os.Exit(1)
	}
}
