package bucket

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// GenerateKey writes a new signing key to path (0600, never overwriting) and
// returns its public key, as a line for the other side's trusted keys file.
func GenerateKey(path string) (string, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		f.Close()
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), f.Close()
}

// LoadKey reads a signing key written by GenerateKey.
func LoadKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: not a signing key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// LoadTrusted reads a trusted keys file: one public key per line, with blank
// lines and # comments allowed.
func LoadTrusted(path string) ([]ed25519.PublicKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var keys []ed25519.PublicKey
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, err := base64.StdEncoding.DecodeString(line)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s:%d: not a public key", path, n)
		}
		keys = append(keys, pub)
	}
	return keys, sc.Err()
}
