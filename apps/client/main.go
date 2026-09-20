package main

import (
	"context"
	"errors"
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
	"bosstransfer/internal/catalogclient"
	"bosstransfer/internal/config"
	"bosstransfer/internal/downloaderconfig"
	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/licenseclient"
	"bosstransfer/internal/localauth"
	"bosstransfer/internal/setup"
	"bosstransfer/internal/transfer"
	"bosstransfer/internal/websession"
)

type clientPageData struct {
	Version    string
	InitialTab string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		runHealthcheck()
		return
	}

	cfg, err := config.LoadClient()
	if err != nil {
		slog.Error("invalid client configuration", "error", err)
		os.Exit(2)
	}

	setupStore, err := setup.NewStore(filepath.Join(cfg.DataDir, "client-setup.json"), setup.Config{CloudDrive2Address: cfg.CloudDrive2Addr, AuthMode: "password"})
	if err != nil {
		slog.Error("invalid client setup", "error", err)
		os.Exit(2)
	}
	qBittorrentStore, err := downloaderconfig.Open(filepath.Join(cfg.DataDir, "qbittorrent.json"))
	if err != nil {
		slog.Error("invalid qBittorrent setup", "error", err)
		os.Exit(2)
	}
	localAuthStore, err := localauth.Open(filepath.Join(cfg.DataDir, "client-auth.json"), localauth.Bootstrap{
		Username: cfg.SetupUser, Password: cfg.SetupPassword, MustChange: cfg.SetupUser == config.DefaultSetupUser && cfg.SetupPassword == config.DefaultSetupPassword,
	}, localauth.Options{})
	if err != nil {
		slog.Error("cannot initialize client web account", "error", err)
		os.Exit(2)
	}
	licenseStore, err := licenseclient.Open(filepath.Join(cfg.DataDir, "client-license.json"), cfg.ManagerURL)
	if err != nil {
		slog.Error("cannot initialize client license identity", "error", err)
		os.Exit(2)
	}
	licenseService := licenseclient.NewService(licenseStore, nil)
	accountStore, err := account115.Open(filepath.Join(cfg.DataDir, "client-115.json"), "")
	if err != nil {
		slog.Error("cannot initialize client 115 account", "error", err)
		os.Exit(2)
	}
	accountService := account115.NewService(accountStore, open115.New(nil))
	webAuth := websession.New(localAuthStore, "bosstransfer_client_session", "/login")
	licenseApp := clientLicenseApp{service: licenseService}
	savedSetup := setupStore.Get()
	sourceDir, _, sourceErr := transfer.SafeJoin(cfg.SourceDir, savedSetup.SourceRelative)
	if sourceErr != nil {
		sourceDir = cfg.SourceDir
	}
	targetDir, _, targetErr := transfer.SafeJoin(cfg.TargetDir, savedSetup.TargetRelative)
	if targetErr != nil {
		targetDir = cfg.TargetDir
	}
	// Transfer paths are derived from fixed Docker roots and the visual setup
	// selection. The legacy config.json is intentionally ignored so an old NAS
	// host path can no longer override a valid container mapping.
	store := transfer.NewInMemoryConfigStore(transfer.Config{
		SourceDir:  sourceDir,
		TargetDir:  targetDir,
		MaxResults: cfg.MaxResults,
	})
	transferService := transfer.NewService(store)
	setupApp := newClientSetupApp(transferService, setupStore, cfg.SourceDir, cfg.TargetDir, cfg.SourceHostPath, cfg.TargetHostPath)
	catalogClient := catalogclient.New(licenseStore, nil)
	accountApp := client115API{account: accountService, catalog: catalogClient}
	downloadApp := newClientDownloadAPI(transferService, catalogClient, licenseService, setupStore, qBittorrentStore, accountService)
	protectLocal := func(handler http.Handler) http.Handler {
		return webAuth.RequirePasswordChanged(handler)
	}
	protectMutation := func(handler http.Handler) http.Handler {
		return webAuth.RequirePasswordChanged(requireSafeMutation(handler))
	}
	protectAuthMutation := func(handler http.Handler) http.Handler {
		return webAuth.Require(requireSafeMutation(handler))
	}
	protectLicensed := func(handler http.Handler) http.Handler {
		return webAuth.RequirePasswordChanged(requireActiveLicense(licenseService, handler))
	}
	protectLicensedMutation := func(handler http.Handler) http.Handler {
		return webAuth.RequirePasswordChanged(requireActiveLicense(licenseService, requireSafeMutation(handler)))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", loginPageHandler(webAuth))
	mux.Handle("/password", webAuth.Require(http.HandlerFunc(clientPasswordPageHandler(webAuth))))
	mux.Handle("/activate", protectLocal(http.HandlerFunc(activationPageHandler(licenseService))))
	mux.Handle("/settings", protectLocal(http.HandlerFunc(clientPageHandler("settings"))))
	mux.Handle("/api/v1/client/auth/login", requireSafeMutation(http.HandlerFunc(webAuth.LoginHandler)))
	mux.Handle("/api/v1/client/auth/logout", protectAuthMutation(http.HandlerFunc(webAuth.LogoutHandler)))
	mux.HandleFunc("/api/v1/client/auth/session", webAuth.SessionHandler)
	mux.Handle("/api/v1/client/auth/credentials", protectAuthMutation(http.HandlerFunc(webAuth.CredentialsHandler)))
	mux.Handle("/api/v1/client/license", protectLocal(http.HandlerFunc(licenseApp.statusHandler)))
	mux.Handle("/api/v1/client/license/activate", protectMutation(http.HandlerFunc(licenseApp.activateHandler)))
	mux.Handle("/api/v1/client/config", protectLocal(http.HandlerFunc(clientConfigHandler(transferService))))
	mux.Handle("/api/v1/client/setup", protectMutation(http.HandlerFunc(setupApp.setupHandler)))
	mux.Handle("/api/v1/client/clouddrive/connect", protectMutation(http.HandlerFunc(setupApp.connectHandler)))
	mux.Handle("/api/v1/client/clouddrive/mounts", protectLocal(http.HandlerFunc(setupApp.mountsHandler)))
	mux.Handle("/api/v1/client/clouddrive/directories", protectLocal(http.HandlerFunc(setupApp.cloudDirectoriesHandler)))
	mux.Handle("/api/v1/client/directories", protectMutation(http.HandlerFunc(setupApp.directoriesHandler)))
	mux.Handle("/api/v1/client/qbittorrent", protectMutation(newQBittorrentSettingsHandler(qBittorrentStore)))
	mux.Handle("/api/v1/client/115", protectLocal(http.HandlerFunc(accountApp.status)))
	mux.Handle("/api/v1/client/115/auth/start", protectLicensedMutation(http.HandlerFunc(accountApp.startAuth)))
	mux.Handle("/api/v1/client/115/auth/status", protectLicensed(http.HandlerFunc(accountApp.authStatus)))
	mux.Handle("/api/v1/client/115/directories", protectLicensed(http.HandlerFunc(accountApp.directories)))
	mux.Handle("/api/v1/client/115/folder", protectLicensedMutation(http.HandlerFunc(accountApp.selectFolder)))
	mux.Handle("/api/v1/client/115/account", protectLicensedMutation(http.HandlerFunc(accountApp.accountAction)))
	mux.Handle("/api/v1/client/search", protectLicensed(http.HandlerFunc(downloadApp.searchHandler)))
	mux.Handle("/api/v1/client/downloaders", protectLicensed(http.HandlerFunc(downloadApp.downloadersHandler)))
	mux.Handle("/api/v1/client/downloads", protectLicensedMutation(http.HandlerFunc(downloadApp.downloadsHandler)))
	mux.Handle("/api/v1/client/downloads/", protectLicensedMutation(http.HandlerFunc(downloadApp.downloadsHandler)))
	mux.Handle("/api/v1/client/summary", protectLocal(httpapi.Method(http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		cfg := transferService.Config()
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{
			"product":     buildinfo.AppName,
			"version":     buildinfo.Version,
			"role":        "client",
			"target_dir":  cfg.TargetDir,
			"max_results": cfg.MaxResults,
			"tasks":       transferService.Tasks(),
		})
	})))
	mux.HandleFunc("/api/v1/health/live", httpapi.Method(http.MethodGet, httpapi.LiveHandler()))
	mux.HandleFunc("/api/v1/health/ready", httpapi.Method(http.MethodGet, httpapi.ReadyHandler(func(*http.Request) []httpapi.Check {
		checks := []httpapi.Check{{Name: "process", Status: "ok", Message: "客户端服务运行中"}}
		for _, check := range transferService.Checks() {
			checks = append(checks, httpapi.Check{Name: check.Name, Status: check.Status, Message: check.Message})
		}
		return checks
	})))
	mux.Handle("/", protectLocal(requireLicensePage(licenseService, http.HandlerFunc(clientPageHandler("download")))))

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
	startLicenseHeartbeat(ctx, licenseService)

	serveResult := make(chan error, 1)
	go func() {
		slog.Info("client starting", "app", buildinfo.AppName, "version", buildinfo.Version, "addr", cfg.Server.Addr)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveResult:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	shutdownErr := shutdownClient(shutdownCtx, server, transferService)
	if serveErr != nil {
		slog.Error("client stopped unexpectedly", "error", serveErr)
		os.Exit(1)
	}
	if shutdownErr != nil {
		slog.Error("client shutdown failed", "error", shutdownErr)
		os.Exit(1)
	}
}

func clientPageHandler(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/settings" {
			httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = clientTemplateV2.Execute(w, clientPageData{
			Version:    buildinfo.Version,
			InitialTab: tab,
		})
	}
}

func clientConfigHandler(service *transfer.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeClientConfig(w, service)
		default:
			w.Header().Set("Allow", http.MethodGet)
			httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		}
	}
}

func writeClientConfig(w http.ResponseWriter, service *transfer.Service) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"config":  service.Config(),
		"checks":  service.Checks(),
		"version": buildinfo.Version,
	})
}

func runHealthcheck() {
	url := os.Getenv("BOSSTRANSFER_HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:18085/api/v1/health/live"
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
