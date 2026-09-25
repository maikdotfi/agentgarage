package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/hosting"
)

// A machine keeps its keys and r2.env in its keys dir, and its databases,
// workspaces and socket in its home. Both are GARAGE_HOME (default ~/.garage),
// except on a host made by garage setup.
const (
	signingKeyFile = "signing.key"  // this side's ed25519 key
	trustedFile    = "trusted.keys" // the other side's public keys, pinned here
	masterKeyFile  = "master.key"   // host only: opens secrets
	recipientFile  = "recipient"    // laptop: what secrets are encrypted to
	r2EnvFile      = "r2.env"       // the bucket, when not in the environment
	socketFile     = "garage.sock"
)

// onHost is whether this machine was made by garage setup.
func onHost() bool {
	fi, err := os.Stat(hosting.Home)
	return err == nil && fi.IsDir()
}

func garageHome() string {
	if h := os.Getenv("GARAGE_HOME"); h != "" {
		return h
	}
	if onHost() {
		return hosting.Home
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".garage"
	}
	return filepath.Join(h, ".garage")
}

func keysDir() string {
	if os.Getenv("GARAGE_HOME") == "" && onHost() {
		return hosting.Keys
	}
	return garageHome()
}

var r2Vars = []string{"GARAGE_R2_ENDPOINT", "GARAGE_R2_BUCKET", "GARAGE_R2_ACCESS_KEY_ID", "GARAGE_R2_SECRET_ACCESS_KEY"}

// r2Store is R2 as the environment says, or else as keys/r2.env does.
func r2Store(keys string) (bucket.Store, error) {
	vals := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(keys, r2EnvFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && !strings.HasPrefix(k, "#") {
			vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	var missing []string
	for _, name := range r2Vars {
		if v := os.Getenv(name); v != "" {
			vals[name] = v
		}
		if vals[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("no bucket: set %s, or put them in %s",
			strings.Join(missing, ", "), filepath.Join(keys, r2EnvFile))
	}
	return bucket.R2(bucket.R2Config{
		Endpoint: vals["GARAGE_R2_ENDPOINT"], Bucket: vals["GARAGE_R2_BUCKET"],
		AccessKeyID: vals["GARAGE_R2_ACCESS_KEY_ID"], SecretAccessKey: vals["GARAGE_R2_SECRET_ACCESS_KEY"],
	}), nil
}

// openBucket is the budgeted client for this machine, signing with this
// side's key and trusting the pinned keys. A nil store is R2.
func openBucket(keys string, store bucket.Store, limits map[string]bucket.Limit) (*bucket.Client, error) {
	if store == nil {
		var err error
		if store, err = r2Store(keys); err != nil {
			return nil, err
		}
	}
	key, err := bucket.LoadKey(filepath.Join(keys, signingKeyFile))
	if err != nil {
		return nil, fmt.Errorf("%w (run garage init)", err)
	}
	var trusted []ed25519.PublicKey
	if trusted, err = bucket.LoadTrusted(filepath.Join(keys, trustedFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return bucket.New(store, key, trusted, limits), nil
}

// configKey is what garage serve runs, written by garage remote config.
const configKey = "config/garage.json"

type garageConfig struct {
	Model    string `json:"model,omitempty"`
	ModelURL string `json:"model_url,omitempty"` // Anthropic-compatible; empty for Anthropic's
	GitName  string `json:"git_name,omitempty"`  // who the dev agent's commits are by
	GitEmail string `json:"git_email,omitempty"`
	// Workspaces maps each workspace's name to the git remote it clones.
	Workspaces map[string]string `json:"workspaces"`
}

// parseConfig reads a config strictly, so a typo fails on the laptop and not
// on the host, and fills in the defaults.
func parseConfig(raw []byte) (garageConfig, error) {
	var cfg garageConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", configKey, err)
	}
	if len(cfg.Workspaces) == 0 {
		return cfg, fmt.Errorf("%s: no workspaces", configKey)
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-5"
	}
	if cfg.GitName == "" {
		cfg.GitName = "garage-dev"
	}
	if cfg.GitEmail == "" {
		cfg.GitEmail = "dev@agentgarage.invalid"
	}
	return cfg, nil
}

func loadConfig(ctx context.Context, c *bucket.Caller) (garageConfig, error) {
	obj, err := c.Get(ctx, configKey)
	if errors.Is(err, bucket.ErrNotFound) {
		return garageConfig{}, fmt.Errorf("no %s in the bucket (write one with garage remote config)", configKey)
	}
	if err != nil {
		return garageConfig{}, err
	}
	return parseConfig(obj.Body)
}

// modelChoice is the model an agent runs: its id, an Anthropic-compatible
// endpoint ("" for Anthropic's), and the name of the secret holding its key.
type modelChoice struct{ ID, URL, Key string }

// agentModel is the model for the agent called name. GARAGE_<NAME>_MODEL,
// _MODEL_URL and _MODEL_KEY win over GARAGE_MODEL and friends, which win over
// the bucket config.
func agentModel(cfg garageConfig, name string, getenv func(string) string) modelChoice {
	pick := func(suffix, fallback string) string {
		for _, k := range []string{"GARAGE_" + strings.ToUpper(name) + "_" + suffix, "GARAGE_" + suffix} {
			if v := getenv(k); v != "" {
				return v
			}
		}
		return fallback
	}
	return modelChoice{ID: pick("MODEL", cfg.Model), URL: pick("MODEL_URL", cfg.ModelURL), Key: pick("MODEL_KEY", "ANTHROPIC_API_KEY")}
}
