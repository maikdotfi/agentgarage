package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
)

const remoteUsage = `usage: garage remote secret NAME   (the value is read from stdin)`

// remote is the laptop side. Today it only writes secrets.
func remote(args []string, stdin io.Reader, _, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "secret" {
		fmt.Fprintln(stderr, remoteUsage)
		return 2
	}
	name := args[1]
	home := garageHome()
	recipient, err := os.ReadFile(filepath.Join(home, recipientFile))
	if err != nil {
		fmt.Fprintf(stderr, "garage remote: %v (copy the host's recipient there)\n", err)
		return 1
	}
	value, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	b, err := openBucket(home, map[string]bucket.Limit{"secrets": {Every: time.Second, Burst: 5}})
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	err = secrets.Put(context.Background(), b.Caller("secrets"),
		strings.TrimSpace(string(recipient)), name, strings.TrimRight(string(value), "\r\n"))
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	return 0
}
