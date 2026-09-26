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
	"sync/atomic"
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
	"agents/grug":     filepath.Join("agents", "grug.db"),
}

// garageWorkspace is the workspace that is the garage's own repo: the one
// dev's deploy tool builds releases from.
const garageWorkspace = "agentgarage"

// errRestart is serve stopping because a new release is installed; systemd
// starts it again, into the new binary.
var errRestart = errors.New("restarting into a new release")

// serveLimits are garage serve's callers of the bucket.
var serveLimits = map[string]bucket.Limit{
	"secrets":   {Every: time.Second, Burst: 5},
	"config":    {Every: time.Second, Burst: 5},
	"backup":    {Every: time.Second, Burst: 10},
	"chat-poll": {Every: time.Second, Burst: 20},
	"release":   {Every: time.Second, Burst: 5},
}

// serve runs the chatroom, the dev and grug agents, the mail relay and the chat UI, and
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
	releases := ""
	if os.Getenv("GARAGE_HOME") == "" && onHost() {
		releases = hosting.Releases
	}
	err = runServe(ctx, serveEnv{home: garageHome(), keys: keysDir(), ui: ui, mailEvery: 10 * time.Second, releases: releases})
	if errors.Is(err, errRestart) {
		fmt.Fprintln(stderr, "garage serve:", err)
		return 0
	}
	if err != nil {
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
	// releases is where new releases are installed, on a host; "" means no
	// polling for releases and no deploys.
	releases string
	model    model.ModelClient // nil is the configured provider
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
	// clientFor is the model client and id for the agent called name.
	clientFor := func(name string) (model.ModelClient, string, error) {
		choice := agentModel(cfg, name, os.Getenv)
		if env.model != nil {
			return env.model, choice.ID, nil
		}
		apiKey, err := master.Get(ctx, b.Caller("secrets"), choice.Key)
		if err != nil && choice.Key != "ANTHROPIC_API_KEY" {
			// The name came from the environment, and may well be a pasted key.
			return nil, "", fmt.Errorf("%s: no secret by the name GARAGE_%s_MODEL_KEY or GARAGE_MODEL_KEY gives; it names a secret, it doesn't hold the key",
				name, strings.ToUpper(name))
		}
		if err != nil {
			return nil, "", err
		}
		m, err := model.New(model.Config{Provider: model.ProviderAnthropic, APIKey: apiKey, BaseURL: choice.URL})
		slog.Info("garage serve: model", "agent", name, "model", choice.ID, "url", choice.URL, "key", choice.Key)
		return m, choice.ID, err
	}
	devModel, devID, err := clientFor(agents.DevName)
	if err != nil {
		return err
	}
	grugModel, grugID, err := clientFor(agents.GrugName)
	if err != nil {
		return err
	}

	var workspaces []*workspace.Workspace
	var deploy func(ctx context.Context, room, sha string) (string, error)
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
		if name == garageWorkspace && env.releases != "" {
			deploy = func(ctx context.Context, room, sha string) (string, error) {
				full, err := hosting.Deploy(ctx, b.Caller("release"), env.releases, ws, sha, room)
				if err != nil {
					return "", err
				}
				return "Released " + full + ". The garage restarts into it once no agent is busy, and posts \"running <sha>\" here when it is back.", nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Join(env.home, "agents"), 0o700); err != nil {
		return err
	}
	devDB, err := openAgentDB(ctx, paths["agents/dev"])
	if err != nil {
		return err
	}
	defer devDB.Close()
	grugDB, err := openAgentDB(ctx, paths["agents/grug"])
	if err != nil {
		return err
	}
	defer grugDB.Close()
	chat, err := chatroom.Open(ctx, paths["garage/chatroom"])
	if err != nil {
		return err
	}
	defer chat.Close()
	// busy counts the agents mid-turn, so a restart waits for them, and says
	// who and where, for the pages.
	var busy atomic.Int64
	devBusy, grugBusy := &ui.Busy{}, &ui.Busy{}
	counted := func(h chatroom.Handler, b *ui.Busy) chatroom.Handler {
		return func(ctx context.Context, m chatroom.Message) {
			busy.Add(1)
			b.Start(m.Room)
			defer func() { b.End(); busy.Add(-1) }()
			h(ctx, m)
		}
	}
	chat.Join(agents.DevName, counted(agents.Dev(agents.DevConfig{
		Chat: chat, Model: devModel, ModelID: devID, Store: turso.New(devDB), Workspaces: workspaces, Deploy: deploy,
	}), devBusy))
	chat.Join(agents.GrugName, counted(agents.Grug(agents.GrugConfig{
		Chat: chat, Model: grugModel, ModelID: grugID, Store: turso.New(grugDB), Workspaces: workspaces,
	}), grugBusy))

	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	restarting := make(chan string, 1)
	wg.Go(func() {
		tick := time.NewTicker(env.mailEvery)
		defer tick.Stop()
		looked, installed := false, ""
		for {
			if err := chat.RelayMail(ctx, b.Caller("chat-poll")); err != nil && ctx.Err() == nil {
				slog.Warn("garage serve: mail relay", "err", err)
			}
			if env.releases != "" && installed == "" {
				sha, err := hosting.Update(ctx, b.Caller("release"), env.releases)
				if err != nil && ctx.Err() == nil {
					slog.Warn("garage serve: release", "err", err)
				}
				if installed = sha; installed != "" {
					slog.Info("garage serve: installed a release; restarting once no agent is busy", "sha", sha)
				} else if !looked {
					// Booted into the release a room deployed: tell it, which wakes dev to check.
					if room, text := hosting.Booted(env.releases); room != "" {
						chat.Post(ctx, room, agents.GarageName, "@"+agents.DevName+" "+text)
					}
				}
				looked = true
			}
			if installed != "" && busy.Load() == 0 {
				restarting <- installed
				cancel()
				return
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
		"agents/dev":      vacuum(devDB),
		"agents/grug":     vacuum(grugDB),
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

	web, err := ui.New(chat,
		ui.Agent{Name: agents.DevName, Model: devID, Store: turso.New(devDB), Busy: devBusy},
		ui.Agent{Name: agents.GrugName, Model: grugID, Store: turso.New(grugDB), Busy: grugBusy},
	)
	if err != nil {
		return err
	}
	// Requests share serve's context, so stopping ends the rooms' open event
	// streams instead of waiting on them.
	webSrv := &http.Server{Handler: web, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
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
	slog.Info("garage serve: listening", "socket", sock, "ui", "http://"+env.ui.Addr().String())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		cancel()
		return err
	}
	select {
	case sha := <-restarting:
		return fmt.Errorf("%w: %s", errRestart, sha)
	default:
		return nil
	}
}

// openAgentDB opens an agent's own database, with metaharness's schema.
func openAgentDB(ctx context.Context, path string) (*sql.DB, error) {
	conn, err := tursodrv.NewConnector(path)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(conn)
	if err := turso.Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// vacuum snapshots a database with VACUUM INTO.
func vacuum(db *sql.DB) hosting.Snapshot {
	return func(ctx context.Context, path string) error {
		if strings.ContainsRune(path, '\'') { // Turso takes only a literal here
			return fmt.Errorf("snapshot path %q has a quote", path)
		}
		_, err := db.ExecContext(ctx, "VACUUM INTO '"+path+"'")
		return err
	}
}
