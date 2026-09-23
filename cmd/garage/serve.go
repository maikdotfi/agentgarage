package main

import (
	"context"
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
	"syscall"
	"time"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb/turso"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/workspace"
)

// serve runs the chatroom and the dev agent, and answers garage chat on the
// unix socket until it is signalled to stop.
func serve(args []string, _ io.Reader, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	modelID := fs.String("model", "claude-sonnet-5", "model the dev agent uses")
	modelURL := fs.String("model-url", "", "Anthropic-compatible API base URL; empty for Anthropic's")
	gitName := fs.String("git-name", "garage-dev", "who the dev agent's commits are by")
	gitEmail := fs.String("git-email", "dev@agentgarage.invalid", "the email on the dev agent's commits")
	remotes := map[string]string{}
	fs.Func("workspace", "a workspace as name=git-remote; repeatable", func(v string) error {
		name, remote, ok := strings.Cut(v, "=")
		if !ok || name == "" || remote == "" {
			return errors.New("want name=git-remote")
		}
		remotes[name] = remote
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(remotes) == 0 {
		fmt.Fprintln(stderr, "garage serve: give at least one -workspace name=git-remote")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := runServe(ctx, garageHome(), serveConfig{
		modelID: *modelID, modelURL: *modelURL, remotes: remotes,
		identity: workspace.Identity{Name: *gitName, Email: *gitEmail},
	})
	if err != nil {
		fmt.Fprintln(stderr, "garage serve:", err)
		return 1
	}
	return 0
}

type serveConfig struct {
	modelID  string
	modelURL string
	remotes  map[string]string
	identity workspace.Identity
}

func runServe(ctx context.Context, home string, cfg serveConfig) error {
	b, err := openBucket(home, map[string]bucket.Limit{"secrets": {Every: time.Second, Burst: 5}})
	if err != nil {
		return err
	}
	master, err := secrets.LoadMaster(filepath.Join(home, masterKeyFile))
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
	m, err := model.New(model.Config{Provider: model.ProviderAnthropic, APIKey: apiKey, BaseURL: cfg.modelURL})
	if err != nil {
		return err
	}

	var workspaces []*workspace.Workspace
	for name, remote := range cfg.remotes {
		ws, err := workspace.Open(ctx, workspace.Config{
			Name: name, Remote: remote, Root: filepath.Join(home, "workspaces"),
			Identity: cfg.identity, Env: map[string]string{"GH_TOKEN": ghToken},
		})
		if err != nil {
			return err
		}
		workspaces = append(workspaces, ws)
	}

	if err := os.MkdirAll(filepath.Join(home, "agents"), 0o700); err != nil {
		return err
	}
	store, err := turso.Open(ctx, filepath.Join(home, "agents", "dev.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	chat, err := chatroom.Open(ctx, filepath.Join(home, "chatroom.db"))
	if err != nil {
		return err
	}
	defer chat.Close()
	chat.Join(agents.DevName, agents.Dev(agents.DevConfig{
		Chat: chat, Model: m, ModelID: cfg.modelID, Store: store, Workspaces: workspaces,
	}))

	sock := filepath.Join(home, socketFile)
	os.Remove(sock) // a socket left by a process that is gone
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	srv := &http.Server{Handler: chat.Handler()}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("garage serve: listening", "socket", sock, "model", cfg.modelID)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
