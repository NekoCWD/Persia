// Command persia keeps distributed repository mirrors in sync and publishes
// notifications to Matrix.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"git.nekocwd.link/neko/persia/internal/config"
	"git.nekocwd.link/neko/persia/internal/engine"
	"git.nekocwd.link/neko/persia/internal/forge"
	"git.nekocwd.link/neko/persia/internal/matrix"
	"git.nekocwd.link/neko/persia/internal/message"
	"git.nekocwd.link/neko/persia/internal/store"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}

	command := os.Args[1]
	if command == "version" {
		fmt.Println(version)
		return 0
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", envOr("PERSIA_CONFIG", "config.yaml"), "path to config file")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "persia:", err)
		return 1
	}

	log := newLogger(cfg.LogLevel)

	switch command {
	case "run":
		return runService(cfg, log)
	case "once":
		return runOnce(cfg, log)
	case "doctor":
		return runDoctor(cfg, log)
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: persia <command> [flags]

commands:
  run      reconcile periodically (default for the container)
  once     run a single reconciliation pass and exit
  doctor   check connectivity and credentials
  version  print the version

flags:
  -config path   configuration file (default: $PERSIA_CONFIG or config.yaml)`)
}

func runService(cfg *config.Config, log *slog.Logger) int {
	app, err := buildApp(cfg, log)
	if err != nil {
		log.Error("cannot initialize", "error", err)
		return 1
	}
	defer app.store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := app.matrix.WhoAmI(ctx); err != nil {
		log.Warn("matrix identity check failed; notifications may not work", "error", err)
	} else {
		log.Info("matrix bot authenticated", "user_id", app.matrix.UserID())
	}
	if err := app.local.Check(ctx); err != nil {
		log.Warn("local forgejo check failed; will retry in the background", "error", err)
	}

	if cfg.HealthAddr != "" {
		go serveHealth(ctx, cfg.HealthAddr, app.engine, log)
	}
	if cfg.ShortlinkAddr != "" {
		go serveHTTP(ctx, cfg, cfg.ShortlinkAddr, app.store, app.matrix, app.engine, log)
	}

	log.Info("persia started", "version", version, "topic", cfg.Topic, "sync_interval", cfg.SyncInterval.Std())
	if err := app.engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("engine stopped", "error", err)
		return 1
	}
	log.Info("persia stopped")
	return 0
}

func runOnce(cfg *config.Config, log *slog.Logger) int {
	app, err := buildApp(cfg, log)
	if err != nil {
		log.Error("cannot initialize", "error", err)
		return 1
	}
	defer app.store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if _, err := app.matrix.WhoAmI(ctx); err != nil {
		log.Warn("matrix identity check failed", "error", err)
	}
	if err := app.engine.Sync(ctx); err != nil {
		log.Error("sync failed", "error", err)
		return 1
	}
	status := app.engine.Status()
	log.Info("sync finished", "managed_repositories", status.ManagedRepos, "at", status.LastSync)
	return 0
}

func runDoctor(cfg *config.Config, log *slog.Logger) int {
	app, err := buildApp(cfg, log)
	if err != nil {
		log.Error("cannot initialize", "error", err)
		return 1
	}
	defer app.store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	failed := false
	report := func(name string, err error) {
		if err != nil {
			failed = true
			fmt.Printf("FAIL  %s: %v\n", name, err)
			return
		}
		fmt.Printf("OK    %s\n", name)
	}

	report("local forgejo", app.local.Check(ctx))
	if userID, err := app.matrix.WhoAmI(ctx); err != nil {
		report("matrix", err)
	} else {
		report("matrix ("+userID+")", nil)
	}
	for name, target := range app.targets {
		report("target "+name, target.Check(ctx))
	}

	repos, err := app.local.ListManagedRepos(ctx, cfg.Topic, cfg.Forgejo.Owners)
	if err != nil {
		report("managed repositories", err)
	} else {
		owners := "any"
		if len(cfg.Forgejo.Owners) > 0 {
			owners = strings.Join(cfg.Forgejo.Owners, ", ")
		}
		fmt.Printf("INFO  managed repositories: %d (owners: %s)\n", len(repos), owners)
		for _, repo := range repos {
			fmt.Printf("      - %s\n", repo.FullName)
		}
	}

	if failed {
		return 1
	}
	return 0
}

type application struct {
	store   *store.Store
	matrix  *matrix.Client
	local   *forge.Forgejo
	targets map[string]forge.Remote
	engine  *engine.Engine
}

func buildApp(cfg *config.Config, log *slog.Logger) (*application, error) {
	st, err := store.Open(cfg.Database)
	if err != nil {
		return nil, err
	}

	timeout := cfg.RequestTimeout.Std()
	mx := matrix.New(cfg.Matrix.Homeserver, cfg.Matrix.AccessToken, cfg.Matrix.UserID, timeout)
	local := forge.NewForgejo("local", cfg.Forgejo.BaseURL, cfg.Forgejo.Token, cfg.Forgejo.Username, false, timeout)

	targets := make(map[string]forge.Remote, len(cfg.Targets))
	for name, target := range cfg.Targets {
		if target.Disabled {
			log.Info("target disabled", "target", name)
			continue
		}
		switch target.Type {
		case "forgejo":
			namespace := target.Namespace
			if namespace == "" {
				namespace = target.Username
			}
			targets[name] = forge.NewForgejo(name, target.BaseURL, target.Token, namespace, target.Private(), timeout)
		case "github":
			targets[name] = forge.NewGitHub(name, target.BaseURL, target.Token, target.Namespace, target.Private(), timeout)
		case "gitlab":
			targets[name] = forge.NewGitLab(name, target.BaseURL, target.Token, target.Namespace, target.Private(), timeout)
		default:
			st.Close()
			return nil, fmt.Errorf("target %q has unsupported type %q", name, target.Type)
		}
	}

	msgs, err := message.Load(cfg.Matrix.MessageTemplates)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("load message templates: %w", err)
	}

	eng := engine.New(cfg, local, targets, mx, st, msgs, log)
	return &application{
		store:   st,
		matrix:  mx,
		local:   local,
		targets: targets,
		engine:  eng,
	}, nil
}

func serveHealth(ctx context.Context, addr string, eng *engine.Engine, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		status := eng.Status()
		payload := map[string]any{
			"ok":            status.LastError == "",
			"last_sync":     status.LastSync,
			"last_error":    status.LastError,
			"managed_repos": status.ManagedRepos,
			"version":       version,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Info("health server listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("health server failed", "error", err)
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func serveHTTP(ctx context.Context, cfg *config.Config, addr string, st *store.Store, mx *matrix.Client, eng *engine.Engine, log *slog.Logger) {
	tmpl, err := loadShortlinkTemplate(cfg)
	if err != nil {
		log.Error("shortlink template cannot be loaded", "error", err)
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := strings.Trim(r.URL.Path, "/")
		if path == "" || path == "index" {
			w.WriteHeader(http.StatusNotFound)
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("not found"))
			return
		}
		repos, _ := st.RepoKeys(r.Context())
		key := matchRepoKey(repos, path)
		if key == "" {
			w.WriteHeader(http.StatusNotFound)
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("repository not found"))
			return
		}
		roomID, _, _ := st.Room(r.Context(), key)
		mirrors, _ := st.Mirrors(r.Context(), key)
		alias, public := roomPublicInfo(r.Context(), st, mx, roomID)
		page := renderRepoPage(tmpl, cfg, key, roomID, mirrors, alias, public, eng.Metadata())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Info("shortlink server listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("shortlink server failed", "error", err)
	}
}

func loadShortlinkTemplate(cfg *config.Config) (*template.Template, error) {
	path := cfg.ShortlinkTemplateFile()
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("shortlink_template is required when shortlink is enabled")
	}
	return template.ParseFiles(path)
}

func renderRepoPage(tmpl *template.Template, cfg *config.Config, repoKey, roomID string, mirrors map[string]store.Mirror, alias string, public bool, meta map[string]engine.RepoMeta) string {
	parts := strings.SplitN(repoKey, "/", 2)
	owner := repoKey
	name := repoKey
	if len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	}
	info := meta[repoKey]
	data := shortlinkPage{
		Repository:  repoKey,
		Owner:       owner,
		Repo:        name,
		Description: info.Description,
		AvatarURL:   info.AvatarURL,
		RoomID:      roomID,
		Alias:       alias,
		Public:      public,
		Mirrors:     make([]shortlinkMirror, 0, len(mirrors)),
	}
	if public {
		data.MatrixLink = cfg.Matrix.RoomLinkFor(alias, roomID, repoKey)
	}
	names := make([]string, 0, len(mirrors))
	for target := range mirrors {
		names = append(names, target)
	}
	sort.Strings(names)
	for _, target := range names {
		m := mirrors[target]
		item := shortlinkMirror{Name: target, Address: m.Address}
		if targetCfg, ok := cfg.Targets[target]; ok {
			item.URL = targetCfg.RepoURL(targetCfg.RepoURLTemplate, target, owner, name, m.Address)
			if len(targetCfg.TemplateData) > 0 {
				item.Data = make(map[string]string, len(targetCfg.TemplateData))
				for key, value := range targetCfg.TemplateData {
					item.Data[key] = value
				}
			}
		}
		data.Mirrors = append(data.Mirrors, item)
	}
	buf := bytes.NewBuffer(nil)
	if err := tmpl.Execute(buf, data); err != nil {
		return "template error: " + err.Error()
	}
	return buf.String()
}

type shortlinkMirror struct {
	Name    string
	URL     string
	Address string
	// Data carries the target's free-form template_data map.
	Data map[string]string
}

type shortlinkPage struct {
	Repository  string
	Owner       string
	Repo        string
	Description string
	AvatarURL   string
	RoomID      string
	Alias       string
	Public      bool
	MatrixLink  string
	Mirrors     []shortlinkMirror
}

func matchRepoKey(keys []string, request string) string {
	for _, key := range keys {
		if key == request {
			return key
		}
	}
	for _, key := range keys {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 && parts[1] == request {
			return key
		}
	}
	return ""
}

func roomPublicInfo(ctx context.Context, st *store.Store, mx *matrix.Client, roomID string) (alias string, public bool) {
	if roomID == "" {
		return "", false
	}
	props, ok, _ := st.RoomProps(ctx, roomID)
	cacheValid := ok && time.Since(props.UpdatedAt) <= 5*time.Minute
	if !cacheValid {
		aliases, _ := mx.RoomAliases(ctx, roomID)
		join, _ := mx.JoinRule(ctx, roomID)
		public = join == "public"
		if len(aliases) > 0 {
			alias = aliases[0]
		}
		_ = st.SaveRoomProps(ctx, store.RoomProps{RoomID: roomID, Alias: alias, Public: public, UpdatedAt: time.Now().UTC()})
	} else {
		alias = props.Alias
		public = props.Public
	}
	if public && alias != "" {
		return alias, true
	}
	return "", false
}
