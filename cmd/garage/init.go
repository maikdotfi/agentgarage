package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
)

// initKeys makes this machine's signing key and, with -master, the master key.
// It never overwrites a key.
func initKeys(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	master := fs.Bool("master", false, "also make the master key that opens secrets (the host)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home := keysDir()
	if err := os.MkdirAll(home, 0o700); err != nil {
		fmt.Fprintln(stderr, "garage init:", err)
		return 1
	}

	pub, err := bucket.GenerateKey(filepath.Join(home, signingKeyFile))
	if err != nil {
		fmt.Fprintln(stderr, "garage init:", err)
		return 1
	}
	fmt.Fprintf(stdout, "signing key: %s\npublic key (add it to the other side's %s):\n  %s\n",
		filepath.Join(home, signingKeyFile), trustedFile, pub)

	if *master {
		recipient, err := secrets.GenerateMaster(filepath.Join(home, masterKeyFile))
		if err != nil {
			fmt.Fprintln(stderr, "garage init:", err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(home, recipientFile), []byte(recipient+"\n"), 0o644); err != nil {
			fmt.Fprintln(stderr, "garage init:", err)
			return 1
		}
		fmt.Fprintf(stdout, "master key: %s (keep one offline copy in a password manager)\nrecipient (put it in the laptop's %s):\n  %s\n",
			filepath.Join(home, masterKeyFile), recipientFile, recipient)
	}
	return 0
}
