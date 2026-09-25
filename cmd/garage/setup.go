package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"runtime"

	"github.com/maikdotfi/agentgarage/hosting"
)

// setup makes this Debian machine a garage host, as root. It is safe to run
// again, which is also how a new binary is deployed by hand.
func setup(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sshFrom := fs.String("ssh-from", "", "the one IP, or range such as 192.168.1.0/24, that may SSH in")
	var trust []string
	fs.Func("trust", "a laptop's public key, as garage init prints it; repeatable", func(v string) error {
		trust = append(trust, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	from, err := netip.ParsePrefix(*sshFrom)
	if ip, ipErr := netip.ParseAddr(*sshFrom); ipErr == nil {
		from, err = netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()), nil
	}
	if err != nil {
		fmt.Fprintln(stderr, "garage setup: give -ssh-from <ip or range>, the one address or range that may SSH in")
		return 2
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "garage setup: run it as root on the Debian host")
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "garage setup:", err)
		return 1
	}
	err = hosting.Setup(context.Background(), hosting.Host{Run: hosting.Exec, Out: stdout},
		hosting.Config{SSHFrom: from, Trust: trust, Binary: self})
	if err != nil {
		fmt.Fprintln(stderr, "garage setup:", err)
		return 1
	}
	return 0
}
