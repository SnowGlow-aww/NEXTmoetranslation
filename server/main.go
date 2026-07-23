package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"moesekai/server/internal/api"
	"moesekai/server/internal/auth"
	"moesekai/server/internal/backup"
	"moesekai/server/internal/config"
	"moesekai/server/internal/db"
	"moesekai/server/internal/files"
	"moesekai/server/internal/filesvc"
	"moesekai/server/internal/searchindex"
	"moesekai/server/internal/sse"
	"moesekai/server/internal/store"
	"moesekai/server/internal/translator"
	"moesekai/server/internal/upstream"
)

func main() {
	// Timestamped logs (UTC) on stdout so `docker logs` shows operational activity.
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("")

	port := envOr("PORT", "8080")
	dbPath := envOr("DB_PATH", "./data/moesekai.db")
	dataDir := envOr("DATA_DIR", "./data")
	webDir := envOr("WEB_DIR", "./web")
	masterKey := os.Getenv("MOESEKAI_MASTER_KEY")
	jwtSecret := envOr("JWT_SECRET", "")
	allowOrigin := envOr("CONSOLE_ORIGIN", "*")

	if err := auth.ValidateJWTSecret(jwtSecret); err != nil {
		fmt.Fprintf(os.Stderr, "Fatal: JWT_SECRET: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		fatal("open db", err)
	}
	defer database.Close()

	cfg, err := config.New(database, masterKey)
	if err != nil {
		fatal("init config", err)
	}
	seedConfigFromEnv(cfg)

	authSvc := auth.New(database, jwtSecret, parseTTL(envOr("TOKEN_TTL_HOURS", "168")))
	if err := seedAdminFromEnv(authSvc); err != nil {
		fatal("seed initial admin", err)
	}

	st := store.New(database)
	es := store.NewEventStore(database)
	gen := files.NewGenerator(st, es, dataDir)

	fileService := filesvc.New(st, es, gen)
	fileService.Start()
	// Regenerate public files whenever the DB changes (debounced inside).
	st.OnChange(fileService.Trigger)

	idx := searchindex.New(st, fileService, cfg,
		parseDurMs(envOr("SEARCH_INDEX_DEBOUNCE_MS", "3600000")),
		parseDurMs(envOr("SEARCH_INDEX_REFRESH_MS", "3600000")))
	idx.Start()
	st.OnChange(idx.Trigger)

	hub := sse.NewHub()

	tr := translator.New(st, es, cfg)
	tr.SetProgress(func(stage, detail string, cur, total int) {
		hub.Broadcast(stage, map[string]any{"detail": detail, "current": cur, "total": total})
	})

	// Upstream watcher: polls current_version.json directly (not GitHub REST API),
	// backs off on raw-content 429s, and triggers CN sync on change.
	useGit := envOr("UPSTREAM_USE_GIT", "false") == "true"
	watcher := upstream.New(cfg, func() error {
		result, err := tr.SyncCNOnly()
		if err != nil {
			return err
		}
		if warning := result.SkippedError(); warning != nil {
			return warning
		}
		return nil
	}, upstream.Options{
		Interval: parseDurMs(envOr("UPSTREAM_POLL_MS", "3600000")),
		GitDir:   filepath.Join(dataDir, "masterdata-mirror"),
		UseGit:   useGit,
	})
	watcher.Start()

	// Backup manager: daily + manual backup/restore to S3 and/or GitHub.
	backupMgr := backup.NewManager(cfg, gen, st, es, filepath.Join(dataDir, "backup-work"))
	backupMgr.StartScheduler()

	apiServer := api.NewServer(st, es, authSvc, cfg, hub, tr, watcher, backupMgr)

	mux := http.NewServeMux()
	apiServer.RegisterRoutes(mux)
	mux.Handle("/files/", fileService.Handler())

	// /translation/* is a backward-compatible alias for /files/translation/*.
	// External sites (e.g. pjsk.moe) fetch translation JSON from this path.
	mux.HandleFunc("/translation/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/files/translation/" + strings.TrimPrefix(r.URL.Path, "/translation/")
		fileService.Handler().ServeHTTP(w, r)
	})

	registerOperationalRoutes(mux, database, authSvc)

	// Catch-all: serve the statically-exported console SPA. More specific routes
	// above (/api/, /files/, /healthz, /sse) take precedence in ServeMux, so "/"
	// only receives page and asset requests. This makes Go the single process —
	// no nginx, no Node.js.
	serveWeb := false
	if st, err := os.Stat(webDir); err == nil && st.IsDir() {
		mux.HandleFunc("/", staticHandler(webDir))
		serveWeb = true
	}

	handler := corsMiddleware(mux, allowOrigin)
	handler = loggingMiddleware(handler)

	log.Printf("moesekai server starting on :%s", port)
	log.Printf("  db:        %s", dbPath)
	log.Printf("  data dir:  %s", dataDir)
	log.Printf("  files:     /files/* (public, cacheable)")
	log.Printf("  compat:    /translation/* (alias for /files/translation/*)")
	log.Printf("  api:       /api/*   (JWT, no-store)")
	if serveWeb {
		log.Printf("  console:   /       (static SPA from %s)", webDir)
	} else {
		log.Printf("  console:   not served (%s not found) — API-only mode", webDir)
	}
	if !cfg.HasMasterKey() {
		log.Println("  WARNING: MOESEKAI_MASTER_KEY not set — secrets cannot be stored")
	}
	httpServer := newHTTPServer(":"+port, handler)
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpServer.ListenAndServe() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			fatal("listen", err)
		}
	case sig := <-signals:
		log.Printf("shutdown requested by %s", sig)
		watcher.Stop()
		backupMgr.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
			_ = httpServer.Close()
		}
		if err := <-serverErr; err != nil && err != http.ErrServerClosed {
			log.Printf("server stopped with error: %v", err)
		}
	}
}

func registerOperationalRoutes(mux *http.ServeMux, database *db.DB, authSvc *auth.Auth) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := database.PingContext(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"status":"not_ready"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ready"}`)
	})
	mux.HandleFunc("/healthz/details", authSvc.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"requests": map[string]uint64{
				"total": httpRequestTotal.Load(), "clientErrors": httpClientErrors.Load(),
				"serverErrors": httpServerErrors.Load(),
			},
		})
	}))
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
}

// seedConfigFromEnv writes settings from env vars on first run only, leaving the
// admin UI authoritative thereafter.
func seedConfigFromEnv(cfg *config.Config) {
	seed := map[string]string{
		config.KeyLLMType:                         os.Getenv("LLM_TYPE"),
		config.KeyGeminiAPIKey:                    os.Getenv("GEMINI_API_KEY"),
		config.KeyGeminiModel:                     os.Getenv("GEMINI_MODEL"),
		config.KeyOpenAIAPIKey:                    os.Getenv("OPENAI_API_KEY"),
		config.KeyOpenAIBaseURL:                   os.Getenv("OPENAI_BASE_URL"),
		config.KeyOpenAIModel:                     os.Getenv("OPENAI_MODEL"),
		config.KeyLLMRequestTimeoutMS:             envOr("LLM_REQUEST_TIMEOUT_MS", "45000"),
		config.KeyLLMMaxRetries:                   envOr("LLM_MAX_RETRIES", "2"),
		config.KeyBatchSize:                       envOr("TRANSLATE_BATCH_SIZE", "20"),
		config.KeyRateDelayMS:                     envOr("TRANSLATE_RATE_DELAY_MS", "800"),
		config.KeyUpstreamRepo:                    envOr("UPSTREAM_REPO", "Team-Haruki/haruki-sekai-master"),
		config.KeyUpstreamBranch:                  envOr("UPSTREAM_BRANCH", "main"),
		config.KeyUpstreamVersionURL:              os.Getenv("UPSTREAM_VERSION_URL"),
		config.KeyUpstreamVersionFallbackURL:      os.Getenv("UPSTREAM_VERSION_FALLBACK_URL"),
		config.KeyUpstreamJPMasterdataURL:         os.Getenv("UPSTREAM_JP_MASTERDATA_URL"),
		config.KeyUpstreamJPMasterdataFallbackURL: os.Getenv("UPSTREAM_JP_MASTERDATA_FALLBACK_URL"),
		config.KeyUpstreamCNMasterdataURL:         os.Getenv("UPSTREAM_CN_MASTERDATA_URL"),
		config.KeyUpstreamCNMasterdataFallbackURL: os.Getenv("UPSTREAM_CN_MASTERDATA_FALLBACK_URL"),
		config.KeyUpstreamJPAssetsURL:             os.Getenv("UPSTREAM_JP_ASSETS_URL"),
		config.KeyUpstreamJPAssetsFallbackURL:     os.Getenv("UPSTREAM_JP_ASSETS_FALLBACK_URL"),
		config.KeyUpstreamCNAssetsURL:             os.Getenv("UPSTREAM_CN_ASSETS_URL"),
		config.KeyUpstreamCNAssetsFallbackURL:     os.Getenv("UPSTREAM_CN_ASSETS_FALLBACK_URL"),
		config.KeyUpstreamFetchConcurrency:        os.Getenv("UPSTREAM_FETCH_CONCURRENCY"),
		config.KeySchedulerOn:                     envOr("TRANSLATE_SCHEDULER_ENABLED", "true"),
		config.KeyBackupGitRepoURL:                os.Getenv("GIT_PUSH_REPO_URL"),
		config.KeyBackupGitBranch:                 envOr("GIT_PUSH_BRANCH", "backup-translations"),
		config.KeyBackupS3Bucket:                  os.Getenv("BACKUP_S3_BUCKET"),
		config.KeyBackupS3Region:                  os.Getenv("BACKUP_S3_REGION"),
		config.KeyBackupS3Endpoint:                os.Getenv("BACKUP_S3_ENDPOINT"),
		config.KeyBackupS3AccessKey:               os.Getenv("BACKUP_S3_ACCESS_KEY"),
		config.KeyBackupS3SecretKey:               os.Getenv("BACKUP_S3_SECRET_KEY"),
	}
	seeded := 0
	for k, v := range seed {
		if v == "" {
			continue
		}
		// Secrets need a master key; skip silently if unavailable.
		if config.IsSecret(k) && !cfg.HasMasterKey() {
			continue
		}
		if ok, err := cfg.SetIfAbsent(k, v); err == nil && ok {
			seeded++
		}
	}
	if seeded > 0 {
		log.Printf("[config] seeded %d settings from environment", seeded)
	}
}

// seedAdminFromEnv guarantees an administrator from TRANSLATOR_ACCOUNTS (legacy
// "user:pass,user2:pass2") or ADMIN_USER/ADMIN_PASSWORD when none exists.
func seedAdminFromEnv(a *auth.Auth) error {
	adminCount, err := a.CountAdmins()
	if err != nil || adminCount > 0 {
		return err
	}
	userCount, err := a.CountUsers()
	if err != nil {
		return err
	}
	created := 0
	adminCreated := false
	if accts := os.Getenv("TRANSLATOR_ACCOUNTS"); accts != "" {
		for _, pair := range strings.Split(accts, ",") {
			parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
			if len(parts) != 2 {
				continue
			}
			role := auth.RoleEditor
			if !adminCreated {
				role = auth.RoleAdmin
			}
			if _, err := a.CreateUser(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), role); err == nil {
				created++
				adminCreated = adminCreated || role == auth.RoleAdmin
			}
		}
	}
	if !adminCreated {
		user := envOr("ADMIN_USER", "admin")
		pass := envOr("ADMIN_PASSWORD", "")
		if pass != "" {
			if _, err := a.CreateUser(user, pass, auth.RoleAdmin); err == nil {
				created++
				adminCreated = true
				log.Printf("[auth] created initial admin %q from env", user)
			}
		}
	}
	if created > 0 {
		log.Printf("[auth] seeded %d account(s) from environment", created)
	}
	if !adminCreated && userCount > 0 {
		return fmt.Errorf("database contains users but no administrator; provide a unique strong ADMIN_USER/ADMIN_PASSWORD")
	}
	return nil
}

func corsMiddleware(next http.Handler, origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /files/* and /translation/* set their own permissive CORS; here we scope console API.
		if !strings.HasPrefix(r.URL.Path, "/files/") && !strings.HasPrefix(r.URL.Path, "/translation/") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseTTL(hours string) time.Duration {
	n, err := strconv.Atoi(hours)
	if err != nil || n <= 0 {
		n = 168
	}
	return time.Duration(n) * time.Hour
}

func parseDurMs(ms string) time.Duration {
	n, err := strconv.Atoi(ms)
	if err != nil || n <= 0 {
		n = 3600000
	}
	return time.Duration(n) * time.Millisecond
}

func fatal(ctx string, err error) {
	fmt.Fprintf(os.Stderr, "Fatal: %s: %v\n", ctx, err)
	os.Exit(1)
}
