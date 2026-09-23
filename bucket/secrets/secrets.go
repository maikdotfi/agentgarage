// Package secrets keeps secrets in the bucket as secrets/<name>.age, encrypted
// to the master key's public recipient. Writing needs only the recipient;
// reading needs the master key, which lives on the host and never in the bucket.
package secrets

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"

	"filippo.io/age"

	"github.com/maikdotfi/agentgarage/bucket"
)

var validName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func key(name string) (string, error) {
	if !validName.MatchString(name) {
		return "", fmt.Errorf("secrets: %q is not a plain name", name)
	}
	return "secrets/" + name + ".age", nil
}

// GenerateMaster writes a new master key to path (0600, never overwriting) and
// returns its public recipient, which is all a writer needs.
func GenerateMaster(path string) (recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(f, id.String()); err != nil {
		f.Close()
		return "", err
	}
	return id.Recipient().String(), f.Close()
}

// Put encrypts value to recipient and writes it as the secret name.
func Put(ctx context.Context, c *bucket.Caller, recipient, name, value string) error {
	k, err := key(name)
	if err != nil {
		return err
	}
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("secrets: recipient: %w", err)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return err
	}
	io.WriteString(w, value)
	if err := w.Close(); err != nil {
		return err
	}
	_, err = c.Put(ctx, k, buf.Bytes())
	return err
}

// Master is the master key, which can read every secret.
type Master struct {
	ids []age.Identity
}

// LoadMaster reads a master key file written by GenerateMaster.
func LoadMaster(path string) (*Master, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Master{ids: ids}, nil
}

// Get reads and decrypts the secret name. The value stays in memory only.
func (m *Master) Get(ctx context.Context, c *bucket.Caller, name string) (string, error) {
	k, err := key(name)
	if err != nil {
		return "", err
	}
	obj, err := c.Get(ctx, k)
	if err != nil {
		return "", err
	}
	r, err := age.Decrypt(bytes.NewReader(obj.Body), m.ids...)
	if err != nil {
		return "", fmt.Errorf("secrets: %s: %w", name, err)
	}
	value, err := io.ReadAll(r)
	return string(value), err
}
