package hosting

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
)

var (
	//go:embed units
	units embed.FS
	//go:embed nftables.conf
	nftablesConf string
	//go:embed sshd.conf
	sshdConf string
)

// packages are what the garage execs, from Debian.
var packages = []string{"ca-certificates", "git", "gh"}

// Host is the machine Setup configures. Root prefixes every path and Run
// execs every privileged command, so a test can stand in for both; on a real
// host Root is "" and Run is Exec.
type Host struct {
	Root string
	Run  func(ctx context.Context, name string, args ...string) (string, error)
	Out  io.Writer // what setup did, and what to pin on the laptop
}

// Config is what a human gives garage setup.
type Config struct {
	SSHFrom netip.Addr // the one IP SSH is open to
	Trust   []string   // laptop public keys to accept signatures from
	Binary  string     // the garage binary to install; usually the one running
}

// Exec runs a command and returns its trimmed output; a failure includes it.
func Exec(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// Setup makes this machine a garage host, or puts right whatever isn't.
// Each step checks first and changes only what it must, so running it again
// is safe; only a changed unit or binary restarts anything.
func Setup(ctx context.Context, h Host, cfg Config) error {
	s := &setup{Host: h, cfg: cfg}
	if err := s.check(); err != nil {
		return err
	}
	for _, step := range []func(context.Context) error{
		s.user, s.packages, s.keys, s.binary, s.units, s.sshd, s.firewall,
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

type setup struct {
	Host
	cfg           Config
	binaryChanged bool
}

func (s *setup) path(p string) string { return filepath.Join(s.Root, p) }

func (s *setup) say(format string, args ...any) { fmt.Fprintf(s.Out, format+"\n", args...) }

// check refuses, before anything is touched, what would leave a broken host
// or lock everyone out of it.
func (s *setup) check() error {
	if !s.cfg.SSHFrom.IsValid() {
		return errors.New("setup: give the one IP that may SSH in")
	}
	for _, k := range s.cfg.Trust {
		if pub, err := base64.StdEncoding.DecodeString(k); err != nil || len(pub) != ed25519.PublicKeySize {
			return fmt.Errorf("setup: %q is not a public key (garage init prints the laptop's)", k)
		}
	}
	if _, err := os.Stat(s.path(Keys + "/r2.env")); err != nil {
		return fmt.Errorf("setup: copy the bucket credentials to %s/r2.env first: %w", Keys, err)
	}
	keys, _ := filepath.Glob(s.path("/home/*/.ssh/authorized_keys"))
	for _, p := range append(keys, s.path("/root/.ssh/authorized_keys")) {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			return nil
		}
	}
	return errors.New("setup: no authorized_keys for root or any user; key-only SSH would lock everyone out")
}

func (s *setup) user(ctx context.Context) error {
	if _, err := s.Run(ctx, "id", "-u", "garage"); err != nil {
		if _, err := s.Run(ctx, "useradd", "--system", "--user-group", "--home-dir", Home,
			"--shell", "/usr/sbin/nologin", "garage"); err != nil {
			return err
		}
		s.say("made the garage user")
	}
	for _, dir := range []string{Home, Keys} {
		if err := os.MkdirAll(s.path(dir), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(s.path(dir), 0o700); err != nil {
			return err
		}
	}
	_, err := s.Run(ctx, "chown", "garage:garage", s.path(Home))
	return err
}

func (s *setup) packages(ctx context.Context) error {
	var missing []string
	for _, p := range packages {
		if out, err := s.Run(ctx, "dpkg-query", "-W", "-f=${Status}", p); err != nil || out != "install ok installed" {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if _, err := s.Run(ctx, "apt-get", "update", "-q"); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-qy"}, missing...)...); err != nil {
		return err
	}
	s.say("installed %s", strings.Join(missing, " "))
	return nil
}

// keys makes whichever of the signing and master keys is missing, trusts the
// laptop, and says what the laptop must pin.
func (s *setup) keys(ctx context.Context) error {
	signing, master := s.path(Keys+"/signing.key"), s.path(Keys+"/master.key")
	var made []string
	if _, err := os.Stat(signing); errors.Is(err, os.ErrNotExist) {
		if _, err := bucket.GenerateKey(signing); err != nil {
			return err
		}
		made = append(made, signing)
	}
	if _, err := os.Stat(master); errors.Is(err, os.ErrNotExist) {
		if _, err := secrets.GenerateMaster(master); err != nil {
			return err
		}
		made = append(made, master)
	}

	trusted := s.path(Keys + "/trusted.keys")
	have, err := os.ReadFile(trusted)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	add := ""
	for _, k := range s.cfg.Trust {
		if !bytes.Contains(have, []byte(k)) {
			add += k + "\n"
		}
	}
	if add != "" {
		if err := os.WriteFile(trusted, append(have, add...), 0o600); err != nil {
			return err
		}
		s.say("trusted %d new laptop key(s)", strings.Count(add, "\n"))
	}

	entries, err := os.ReadDir(s.path(Keys))
	if err != nil {
		return err
	}
	for _, e := range entries { // readable by the garage user and nobody else
		if err := os.Chmod(filepath.Join(s.path(Keys), e.Name()), 0o600); err != nil {
			return err
		}
	}
	if _, err := s.Run(ctx, "chown", "-R", "garage:garage", s.path(Keys)); err != nil {
		return err
	}

	key, err := bucket.LoadKey(signing)
	if err != nil {
		return err
	}
	m, err := secrets.LoadMaster(master)
	if err != nil {
		return err
	}
	if len(made) > 0 {
		s.say("made %s: keep an offline copy of each, a rebuilt host needs them", strings.Join(made, " and "))
	}
	s.say("host public key (add it to the laptop's trusted.keys):\n  %s\nmaster recipient (put it in the laptop's recipient):\n  %s",
		base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), m.Recipient())
	return nil
}

// binary installs the given binary as a release named by its content, and
// points serve-current at it.
func (s *setup) binary(context.Context) error {
	raw, err := os.ReadFile(s.cfg.Binary)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	rel := filepath.Join("releases", hex.EncodeToString(sum[:])[:12], "garage")
	dest := s.path(filepath.Join("/opt/garage", rel))
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest+".new", raw, 0o755); err != nil {
			return err
		}
		if err := os.Rename(dest+".new", dest); err != nil {
			return err
		}
	}
	if s.binaryChanged, err = s.link(rel, "/opt/garage/serve-current"); err != nil {
		return err
	}
	if s.binaryChanged {
		s.say("serve-current is now %s", rel)
	}
	_, err = s.link("/opt/garage/serve-current", "/usr/local/bin/garage")
	return err
}

// link points the symlink at path to target, atomically, and says whether it
// had to.
func (s *setup) link(target, path string) (bool, error) {
	if got, err := os.Readlink(s.path(path)); err == nil && got == target {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path(path)), 0o755); err != nil {
		return false, err
	}
	os.Remove(s.path(path) + ".new")
	if err := os.Symlink(target, s.path(path)+".new"); err != nil {
		return false, err
	}
	return true, os.Rename(s.path(path)+".new", s.path(path))
}

func (s *setup) units(ctx context.Context) error {
	entries, err := units.ReadDir("units")
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	for _, e := range entries {
		want, _ := units.ReadFile("units/" + e.Name())
		if c, err := s.writeIfChanged("/etc/systemd/system/"+e.Name(), want, 0o644); err != nil {
			return err
		} else if c {
			changed[e.Name()] = true
		}
	}
	if len(changed) > 0 {
		if _, err := s.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := s.Run(ctx, "systemctl", "enable", "--now", "garage-serve.service", "garage-backup.timer"); err != nil {
		return err
	}
	if changed["garage-serve.service"] || s.binaryChanged {
		if _, err := s.Run(ctx, "systemctl", "restart", "garage-serve.service"); err != nil {
			return err
		}
		s.say("restarted garage serve")
	}
	if changed["garage-backup.timer"] {
		if _, err := s.Run(ctx, "systemctl", "restart", "garage-backup.timer"); err != nil {
			return err
		}
	}
	return nil
}

// sshd makes SSH key-only. A config sshd rejects is taken back out before
// anything reloads it.
func (s *setup) sshd(ctx context.Context) error {
	path := "/etc/ssh/sshd_config.d/00-garage.conf"
	old, oldErr := os.ReadFile(s.path(path))
	changed, err := s.writeIfChanged(path, []byte(sshdConf), 0o644)
	if err != nil || !changed {
		return err
	}
	if _, err := s.Run(ctx, "sshd", "-t"); err != nil {
		if oldErr == nil {
			os.WriteFile(s.path(path), old, 0o644)
		} else {
			os.Remove(s.path(path))
		}
		return fmt.Errorf("setup: sshd rejects the key-only config, left it as it was: %w", err)
	}
	if _, err := s.Run(ctx, "systemctl", "reload", "ssh"); err != nil {
		return err
	}
	s.say("sshd is key-only")
	return nil
}

// firewall drops everything inbound but SSH from the one IP. The ruleset is
// checked before it replaces the old one, and loaded only then.
func (s *setup) firewall(ctx context.Context) error {
	var want bytes.Buffer
	family := "ip"
	if s.cfg.SSHFrom.Is6() && !s.cfg.SSHFrom.Is4In6() {
		family = "ip6"
	}
	template.Must(template.New("nft").Parse(nftablesConf)).Execute(&want,
		map[string]string{"Family": family, "SSHFrom": s.cfg.SSHFrom.Unmap().String()})
	path := s.path("/etc/nftables.conf")
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want.Bytes()) {
		return nil
	}
	if err := os.WriteFile(path+".new", want.Bytes(), 0o644); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "nft", "-c", "-f", path+".new"); err != nil {
		os.Remove(path + ".new")
		return fmt.Errorf("setup: nft rejects the ruleset, left the firewall as it was: %w", err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "nft", "-f", path); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "systemctl", "enable", "nftables.service"); err != nil {
		return err
	}
	s.say("firewall drops everything inbound but SSH from %s", s.cfg.SSHFrom)
	return nil
}

func (s *setup) writeIfChanged(path string, want []byte, mode os.FileMode) (bool, error) {
	if have, err := os.ReadFile(s.path(path)); err == nil && bytes.Equal(have, want) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path(path)), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(s.path(path), want, mode)
}
