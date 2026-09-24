package hosting_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/hosting"
)

// machine is a pretend Debian host: files under a temp root, and commands
// that are recorded and remember just enough to answer the next check.
type machine struct {
	root   string
	ran    []string
	user   bool
	pkgs   bool
	failOn string // a command that fails, such as "sshd -t"
	out    bytes.Buffer
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	m := &machine{root: t.TempDir()}
	m.write(t, "/etc/garage/r2.env", "GARAGE_R2_BUCKET=garage\n")
	m.write(t, "/root/.ssh/authorized_keys", "ssh-ed25519 AAAA mike\n")
	return m
}

func (m *machine) write(t *testing.T, path, content string) {
	t.Helper()
	p := filepath.Join(m.root, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (m *machine) read(path string) string {
	b, _ := os.ReadFile(filepath.Join(m.root, path))
	return string(b)
}

func (m *machine) run(_ context.Context, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	m.ran = append(m.ran, cmd)
	if m.failOn != "" && strings.HasPrefix(cmd, m.failOn) {
		return "", errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(cmd, "id "):
		if !m.user {
			return "", errors.New("no such user")
		}
	case strings.HasPrefix(cmd, "useradd "):
		m.user = true
	case strings.HasPrefix(cmd, "dpkg-query "):
		if !m.pkgs {
			return "", errors.New("not installed")
		}
		return "install ok installed", nil
	case strings.Contains(cmd, "apt-get install"):
		m.pkgs = true
	}
	return "", nil
}

// ranLike is every command run so far that starts with prefix.
func (m *machine) ranLike(prefix string) []string {
	var out []string
	for _, c := range m.ran {
		if strings.HasPrefix(c, prefix) || strings.Contains(c, " "+prefix) {
			out = append(out, c)
		}
	}
	return out
}

func laptopKey(t *testing.T) string {
	pub, _, _ := ed25519.GenerateKey(nil)
	return base64.StdEncoding.EncodeToString(pub)
}

func binary(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "garage")
	os.WriteFile(p, []byte(content), 0o755)
	return p
}

func (m *machine) setup(t *testing.T, cfg hosting.Config) error {
	t.Helper()
	m.ran = nil
	m.out.Reset()
	return hosting.Setup(context.Background(), hosting.Host{Root: m.root, Run: m.run, Out: &m.out}, cfg)
}

func config(t *testing.T, bin, trust string) hosting.Config {
	return hosting.Config{SSHFrom: netip.MustParseAddr("203.0.113.7"), Trust: []string{trust}, Binary: bin}
}

func TestSetupMakesAHostFromNothing(t *testing.T) {
	m := newMachine(t)
	laptop := laptopKey(t)
	bin := binary(t, "garage v1")

	if err := m.setup(t, config(t, bin, laptop)); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"useradd", "apt-get install", "systemctl enable --now garage-serve.service garage-backup.timer", "systemctl reload ssh", "nft -f"} {
		if len(m.ranLike(want)) == 0 {
			t.Errorf("setup never ran %q; ran:\n%s", want, strings.Join(m.ran, "\n"))
		}
	}
	if apt := m.ranLike("apt-get install"); len(apt) == 0 || !strings.Contains(apt[0], "git") || !strings.Contains(apt[0], "gh") {
		t.Errorf("apt-get = %q, want git and gh", apt)
	}
	if !strings.Contains(m.read("/etc/garage/trusted.keys"), laptop) {
		t.Error("the laptop's key is not trusted")
	}
	for _, f := range []string{"signing.key", "master.key"} {
		if m.read("/etc/garage/"+f) == "" {
			t.Errorf("no %s", f)
		}
	}
	if !strings.Contains(m.out.String(), "age1") || !strings.Contains(m.out.String(), "public key") {
		t.Errorf("output does not show what to pin on the laptop:\n%s", m.out.String())
	}
	if got := m.read("/opt/garage/serve-current"); got != "garage v1" {
		t.Errorf("serve-current runs %q, want the binary setup was given", got)
	}
	if !strings.Contains(m.read("/etc/systemd/system/garage-serve.service"), "/opt/garage/serve-current serve") {
		t.Error("no serve unit running serve-current")
	}
	if m.read("/etc/systemd/system/garage-backup.timer") == "" {
		t.Error("no backup timer")
	}
	nft := m.read("/etc/nftables.conf")
	if !strings.Contains(nft, "policy drop") || !strings.Contains(nft, "ip saddr 203.0.113.7 tcp dport 22 accept") {
		t.Errorf("firewall does not drop everything but SSH from the one IP:\n%s", nft)
	}
	if sshd := m.read("/etc/ssh/sshd_config.d/00-garage.conf"); !strings.Contains(sshd, "PasswordAuthentication no") {
		t.Errorf("sshd is not key-only:\n%s", sshd)
	}
}

func TestSetupAgainChangesNothing(t *testing.T) {
	m := newMachine(t)
	cfg := config(t, binary(t, "garage v1"), laptopKey(t))
	m.setup(t, cfg)
	keys := m.read("/etc/garage/signing.key") + m.read("/etc/garage/master.key")
	trusted := m.read("/etc/garage/trusted.keys")

	if err := m.setup(t, cfg); err != nil {
		t.Fatal(err)
	}

	for _, disruptive := range []string{"useradd", "apt-get", "systemctl restart", "systemctl reload", "systemctl daemon-reload", "nft -f"} {
		if got := m.ranLike(disruptive); len(got) > 0 {
			t.Errorf("a second run ran %q", got)
		}
	}
	if m.read("/etc/garage/signing.key")+m.read("/etc/garage/master.key") != keys {
		t.Error("a second run replaced the keys")
	}
	if m.read("/etc/garage/trusted.keys") != trusted {
		t.Error("a second run changed the trusted keys")
	}
	if !strings.Contains(m.out.String(), "age1") {
		t.Error("a second run does not show what to pin")
	}
}

func TestANewBinaryRestartsOnlyServe(t *testing.T) {
	m := newMachine(t)
	laptop := laptopKey(t)
	m.setup(t, config(t, binary(t, "garage v1"), laptop))

	if err := m.setup(t, config(t, binary(t, "garage v2"), laptop)); err != nil {
		t.Fatal(err)
	}

	if got := m.ranLike("systemctl restart"); !slices.Equal(got, []string{"systemctl restart garage-serve.service"}) {
		t.Errorf("restarted %q, want only serve", got)
	}
	if got := m.read("/opt/garage/serve-current"); got != "garage v2" {
		t.Errorf("serve-current runs %q", got)
	}
	releases, _ := os.ReadDir(filepath.Join(m.root, "opt/garage/releases"))
	if len(releases) != 2 {
		t.Errorf("%d releases, want the old one kept for rollback", len(releases))
	}
}

func TestAnSSHDConfigThatFailsItsCheckIsRolledBack(t *testing.T) {
	m := newMachine(t)
	m.failOn = "sshd -t"

	if err := m.setup(t, config(t, binary(t, "garage v1"), laptopKey(t))); err == nil {
		t.Fatal("setup succeeded with a broken sshd config")
	}
	if _, err := os.Stat(filepath.Join(m.root, "etc/ssh/sshd_config.d/00-garage.conf")); err == nil {
		t.Error("the config that failed its check was left in place")
	}
	if got := m.ranLike("systemctl reload ssh"); len(got) > 0 {
		t.Error("sshd was reloaded with a config that failed its check")
	}
}

func TestAFirewallThatFailsItsCheckIsNotLoaded(t *testing.T) {
	m := newMachine(t)
	m.write(t, "/etc/nftables.conf", "# the old rules\n")
	m.failOn = "nft -c"

	if err := m.setup(t, config(t, binary(t, "garage v1"), laptopKey(t))); err == nil {
		t.Fatal("setup succeeded with a broken ruleset")
	}
	if got := m.read("/etc/nftables.conf"); got != "# the old rules\n" {
		t.Errorf("nftables.conf = %q, want the old rules", got)
	}
	if got := m.ranLike("nft -f"); len(got) > 0 {
		t.Error("a ruleset that failed its check was loaded")
	}
}

func TestSetupRefusesBeforeTouchingAnything(t *testing.T) {
	for name, spoil := range map[string]func(t *testing.T, m *machine, cfg *hosting.Config){
		"no r2.env": func(t *testing.T, m *machine, _ *hosting.Config) {
			os.Remove(filepath.Join(m.root, "etc/garage/r2.env"))
		},
		"no SSH keys, so key-only SSH locks everyone out": func(t *testing.T, m *machine, _ *hosting.Config) {
			os.Remove(filepath.Join(m.root, "root/.ssh/authorized_keys"))
		},
		"a trusted key that isn't one": func(t *testing.T, m *machine, cfg *hosting.Config) {
			cfg.Trust = []string{"not-a-key"}
		},
		"no IP to allow SSH from": func(t *testing.T, m *machine, cfg *hosting.Config) {
			cfg.SSHFrom = netip.Addr{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			cfg := config(t, binary(t, "garage v1"), laptopKey(t))
			spoil(t, m, &cfg)

			if err := m.setup(t, cfg); err == nil {
				t.Fatal("setup succeeded")
			}
			if len(m.ran) > 0 {
				t.Errorf("setup ran %q before refusing", m.ran)
			}
		})
	}
}

func TestSSHFromAnIPv6AddressIsAllowed(t *testing.T) {
	m := newMachine(t)
	cfg := config(t, binary(t, "garage v1"), laptopKey(t))
	cfg.SSHFrom = netip.MustParseAddr("2001:db8::7")

	if err := m.setup(t, cfg); err != nil {
		t.Fatal(err)
	}
	if nft := m.read("/etc/nftables.conf"); !strings.Contains(nft, "ip6 saddr 2001:db8::7 tcp dport 22 accept") {
		t.Errorf("firewall:\n%s", nft)
	}
}
