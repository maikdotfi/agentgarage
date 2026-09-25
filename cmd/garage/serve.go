package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tursodrv "turso.tech/database/tursogo"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/hosting"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb/turso"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/ui"
	"github.com/maikdotfi/agentgarage/workspace"
)

// databases is every SQLite file garage serve owns: bucket prefix to its path
// under the home. Backups and restores cover exactly these.
var databases = map[string]string{
	"garage/chatroom": "chatroom.db",
	"agents/dev":      filepath.Join("agents", "dev.db"),
}

// serveLimits are garage serve's callers of the bucket.
var serveLimits = map[string]bucket.Limit{
	"secrets":   {Every: time.Second, Burst: 5},
	"config":    {Every: time.Second, Burst: 5},
	"backup":    {Every: time.Second, Burst: 10},
	"chat-poll": {Every: time.Second, Burst: 20},
}

// serve runs the chatroom, the dev agent, the mail relay and the chat UI, and
// answers on the unix socket until it is signalled to stop. Its config is in
// the bucket.
func serve(args []string, _ io.Reader, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("http", "0.0.0.0:8080", "where the chat UI listens; garage setup opens :8080 to the SSH range")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "garage serve takes no arguments; its config is %s in the bucket\n", configKey)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ui, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(stderr, "garage serve:", err)
		return 1
	}
	if err := runServe(ctx, serveEnv{home: garageHome(), keys: keysDir(), ui: ui, mailEvery: 10 * time.Second}); err != nil {
		fmt.Fprintln(stderr, "garage serve:", err)
		return 1
	}
	return 0
}

type serveEnv struct {
	home, keys string
	store      bucket.Store // nil is R2
	ui         net.Listener // the chat UI's; runServe closes it
	mailEvery  time.Duration
}

func runServe(ctx context.Context, env serveEnv) error {
	if env.ui != nil {
		defer env.ui.Close()
	}
	b, err := openBucket(env.keys, env.store, serveLimits)
	if err != nil {
		return err
	}
	paths := map[string]string{}
	for owner, rel := range databases {
		paths[owner] = filepath.Join(env.home, rel)
	}
	restored, err := hosting.Restore(ctx, b.Caller("backup"), paths)
	if err != nil {
		return err
	}
	for _, p := range restored {
		slog.Info("garage serve: restored from the bucket", "database", p)
	}
	cfg, err := loadConfig(ctx, b.Caller("config"))
	if err != nil {
		return err
	}

	master, err := secrets.LoadMaster(filepath.Join(env.keys, masterKeyFile))
	if err != nil {
		return fmt.Errorf("%w (run garage init -master)", err)
	}
	ghToken, err := master.Get(ctx, b.Caller("secrets"), "GH_TOKEN")
	if err != nil {
		return err
	}
	apiKey, err := master.Get(ctx, b.Caller("secrets"), "ANTHROPIC_API_KEY")
	if err != nil {
		return err
	}
	m, err := model.New(model.Config{Provider: model.ProviderAnthropic, APIKey: apiKey, BaseURL: cfg.ModelURL})
	if err != nil {
		return err
	}

	var workspaces []*workspace.Workspace
	for name, remote := range cfg.Workspaces {
		ws, err := workspace.Open(ctx, workspace.Config{
			Name: name, Remote: remote, Root: filepath.Join(env.home, "workspaces"),
			Identity: workspace.Identity{Name: cfg.GitName, Email: cfg.GitEmail},
			Env:      map[string]string{"GH_TOKEN": ghToken},
		})
		if err != nil {
			return err
		}
		workspaces = append(workspaces, ws)
	}

	if err := os.MkdirAll(filepath.Join(env.home, "agents"), 0o700); err != nil {
		return err
	}
	conn, err := tursodrv.NewConnector(paths["agents/dev"])
	if err != nil {
		return err
	}
	devDB := sql.OpenDB(conn)
	defer devDB.Close()
	if err := turso.Migrate(ctx, devDB); err != nil {
		return err
	}
	chat, err := chatroom.Open(ctx, paths["garage/chatroom"])
	if err != nil {
		return err
	}
	defer chat.Close()
	chat.Join(agents.DevName, agents.Dev(agents.DevConfig{
		Chat: chat, Model: m, ModelID: cfg.Model, Store: turso.New(devDB), Workspaces: workspaces,
	}))

	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wg.Go(func() {
		tick := time.NewTicker(env.mailEvery)
		defer tick.Stop()
		for {
			if err := chat.RelayMail(ctx, b.Caller("chat-poll")); err != nil && ctx.Err() == nil {
				slog.Warn("garage serve: mail relay", "err", err)
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return
			}
		}
	})

	snapshots := map[string]hosting.Snapshot{
		"garage/chatroom": chat.Snapshot,
		"agents/dev": func(ctx context.Context, path string) error {
			if strings.ContainsRune(path, '\'') { // Turso takes only a literal here
				return fmt.Errorf("snapshot path %q has a quote", path)
			}
			_, err := devDB.ExecContext(ctx, "VACUUM INTO '"+path+"'")
			return err
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/rooms/", chat.Handler())
	mux.HandleFunc("POST /backup", func(w http.ResponseWriter, r *http.Request) {
		day := time.Now().UTC().Format(time.DateOnly)
		written, err := hosting.Backup(r.Context(), b.Caller("backup"), day, snapshots)
		if err != nil {
			slog.Error("garage serve: backup", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, strings.Join(written, "\n"))
	})

	web, err := ui.New(chat)
	if err != nil {
		return err
	}
	webSrv := &http.Server{Handler: web, ReadHeaderTimeout: 10 * time.Second}
	wg.Go(func() {
		if err := webSrv.Serve(env.ui); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("garage serve: chat UI", "err", err)
			cancel()
		}
	})

	sock := filepath.Join(env.home, socketFile)
	os.Remove(sock) // a socket left by a process that is gone
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	srv := &http.Server{Handler: mux}
	wg.Go(func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
		webSrv.Shutdown(shutdown)
	})
	slog.Info("garage serve: listening", "socket", sock, "ui", "http://"+env.ui.Addr().String(), "model", cfg.Model)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		cancel()
		return err
	}
	return nil
}
