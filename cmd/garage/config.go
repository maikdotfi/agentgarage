package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/maikdotfi/agentgarage/bucket"
)

// Everything a machine keeps about itself lives in GARAGE_HOME (default
// ~/.garage): its keys, its databases, its workspaces and the socket.
const (
	signingKeyFile = "signing.key"  // this side's ed25519 key
	trustedFile    = "trusted.keys" // the other side's public keys, pinned here
	masterKeyFile  = "master.key"   // host only: opens secrets
	recipientFile  = "recipient"    // laptop: what secrets are encrypted to
	socketFile     = "garage.sock"
)

func garageHome() string {
	if h := os.Getenv("GARAGE_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".garage"
	}
	return filepath.Join(h, ".garage")
}

// openBucket is the budgeted client for this machine: R2 from the environment,
// signing with this side's key and trusting the pinned keys.
func openBucket(home string, limits map[string]bucket.Limit) (*bucket.Client, error) {
	cfg := bucket.R2Config{
		Endpoint:        os.Getenv("GARAGE_R2_ENDPOINT"),
		Bucket:          os.Getenv("GARAGE_R2_BUCKET"),
		AccessKeyID:     os.Getenv("GARAGE_R2_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("GARAGE_R2_SECRET_ACCESS_KEY"),
	}
	var missing []string
	for name, v := range map[string]string{
		"GARAGE_R2_ENDPOINT": cfg.Endpoint, "GARAGE_R2_BUCKET": cfg.Bucket,
		"GARAGE_R2_ACCESS_KEY_ID": cfg.AccessKeyID, "GARAGE_R2_SECRET_ACCESS_KEY": cfg.SecretAccessKey,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("no bucket: set %s", strings.Join(missing, ", "))
	}
	key, err := bucket.LoadKey(filepath.Join(home, signingKeyFile))
	if err != nil {
		return nil, fmt.Errorf("%w (run garage init)", err)
	}
	var trusted []ed25519.PublicKey
	if trusted, err = bucket.LoadTrusted(filepath.Join(home, trustedFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return bucket.New(bucket.R2(cfg), key, trusted, limits), nil
}
