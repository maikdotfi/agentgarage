package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket/secrets"
)

// modelRequest is what an Anthropic-compatible endpoint was asked.
type modelRequest struct{ path, key, model string }

// fakeEndpoint refuses every request, after noting who asked for what.
func fakeEndpoint(t *testing.T) (string, <-chan modelRequest) {
	t.Helper()
	got := make(chan modelRequest, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		got <- modelRequest{r.URL.Path, r.Header.Get("x-api-key"), body.Model}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, got
}

func TestServeTakesTheAgentsModelFromTheEnvironment(t *testing.T) {
	endpoint, got := fakeEndpoint(t)
	t.Setenv("GARAGE_MODEL_URL", endpoint)
	t.Setenv("GARAGE_MODEL_KEY", "OLLAMA_API_KEY")
	t.Setenv("GARAGE_DEV_MODEL", "glm-5:cloud")
	h := newHost(t)
	h.configure(t)
	recipient, _ := os.ReadFile(filepath.Join(h.home, recipientFile))
	if err := secrets.Put(context.Background(), h.laptop, strings.TrimSpace(string(recipient)), "OLLAMA_API_KEY", "ollama-key"); err != nil {
		t.Fatal(err)
	}
	h.serve(t)

	socketClient(filepath.Join(h.home, socketFile)).post(context.Background(), "fix", "mike", "@dev hello")

	select {
	case r := <-got:
		if r.path != "/v1/messages" || r.key != "ollama-key" || r.model != "glm-5:cloud" {
			t.Errorf("dev asked %+v, want /v1/messages with ollama-key for glm-5:cloud", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dev never called the endpoint from the environment")
	}
}

func TestAnAgentsOwnVariablesWinOverTheShared(t *testing.T) {
	cfg := garageConfig{Model: "claude-sonnet-5", ModelURL: "https://api.example"}
	env := map[string]string{
		"GARAGE_MODEL":          "shared",
		"GARAGE_GRUG_MODEL":     "grugs",
		"GARAGE_GRUG_MODEL_URL": "https://ollama.com",
		"GARAGE_GRUG_MODEL_KEY": "OLLAMA_API_KEY",
	}
	getenv := func(k string) string { return env[k] }

	dev, grug := agentModel(cfg, "dev", getenv), agentModel(cfg, "grug", getenv)

	if want := (modelChoice{ID: "shared", URL: "https://api.example", Key: "ANTHROPIC_API_KEY"}); dev != want {
		t.Errorf("dev: %+v, want %+v", dev, want)
	}
	if want := (modelChoice{ID: "grugs", URL: "https://ollama.com", Key: "OLLAMA_API_KEY"}); grug != want {
		t.Errorf("grug: %+v, want %+v", grug, want)
	}
}
