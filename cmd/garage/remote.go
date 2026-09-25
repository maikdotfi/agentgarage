package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/mail"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
	"github.com/maikdotfi/agentgarage/hosting"
)

const remoteUsage = `usage:
  garage remote secret NAME             write a secret; the value is read from stdin
  garage remote config                  write what garage serve runs; JSON on stdin
  garage remote chat [-room R] [-as A]  talk to the host through bucket mail
  garage remote release [-point SHA]    build this checkout for the host and release it,
                                        or point the host back at an earlier release`

// remote is the laptop side: it reaches the host only through the bucket.
func remote(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, remoteUsage)
		return 2
	}
	switch args[0] {
	case "secret":
		return remoteSecret(args[1:], stdin, stderr)
	case "chat":
		return remoteChatCmd(args[1:], stdin, stdout, stderr)
	case "release":
		return remoteReleaseCmd(args[1:], stdout, stderr)
	case "config":
		b, err := openBucket(keysDir(), nil, map[string]bucket.Limit{"config": {Every: time.Second, Burst: 5}})
		if err == nil {
			err = remoteConfig(context.Background(), b.Caller("config"), stdin)
		}
		if err != nil {
			fmt.Fprintln(stderr, "garage remote:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stderr, remoteUsage)
	return 2
}

func remoteSecret(args []string, stdin io.Reader, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, remoteUsage)
		return 2
	}
	name := args[0]
	keys := keysDir()
	recipient, err := os.ReadFile(filepath.Join(keys, recipientFile))
	if err != nil {
		fmt.Fprintf(stderr, "garage remote: %v (copy the host's recipient there)\n", err)
		return 1
	}
	value, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	b, err := openBucket(keys, nil, map[string]bucket.Limit{"secrets": {Every: time.Second, Burst: 5}})
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

func remoteChatCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	room := fs.String("room", "garage", "room to talk in")
	as := fs.String("as", os.Getenv("USER"), "who you are in the room")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home := garageHome()
	b, err := openBucket(keysDir(), nil, map[string]bucket.Limit{"mail": {Every: time.Second, Burst: 10}})
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = remoteChat(ctx, b.Caller("mail"), filepath.Join(home, "mail.json"), *room, *as, 10*time.Second, stdin, stdout)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	return 0
}

func remoteReleaseCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote release", flag.ContinueOnError)
	fs.SetOutput(stderr)
	point := fs.String("point", "", "point releases/current at this earlier release instead of building one")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, remoteUsage)
		return 2
	}
	b, err := openBucket(keysDir(), nil, map[string]bucket.Limit{"release": {Every: time.Second, Burst: 5}})
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	ctx := context.Background()
	sha := *point
	if sha != "" {
		err = hosting.Point(ctx, b.Caller("release"), sha)
	} else {
		sha, err = remoteRelease(ctx, b.Caller("release"), ".")
	}
	if err != nil {
		fmt.Fprintln(stderr, "garage remote:", err)
		return 1
	}
	fmt.Fprintf(stdout, "releases/current is %s; the host installs it on its next poll and restarts\n", sha)
	return 0
}

// remoteRelease builds the commit checked out in repo as a linux/amd64
// garage and releases it through the bucket, named by that commit.
// Uncommitted work is refused, so the name means what it says.
func remoteRelease(ctx context.Context, c *bucket.Caller, repo string) (string, error) {
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("%s is not a git checkout: %w", repo, err)
	}
	if status != "" {
		return "", fmt.Errorf("commit your work first, a release is named by its commit:\n%s", status)
	}
	sha, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "garage-release")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(dir, "garage"), "./cmd/garage")
	build.Dir = repo
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	bin, err := os.ReadFile(filepath.Join(dir, "garage"))
	if err != nil {
		return "", err
	}
	return sha, hosting.Publish(ctx, c, sha, bin)
}

// remoteConfig checks a config the way garage serve will read it, and only
// then writes it to the bucket.
func remoteConfig(ctx context.Context, c *bucket.Caller, stdin io.Reader) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if _, err := parseConfig(raw); err != nil {
		return err
	}
	_, err = c.Put(ctx, configKey, raw)
	return err
}

// cursors is where this laptop is in each mailbox: the next number to send
// and the next to read. The bucket keeps no cursor, so losing this file means
// starting again from zero.
type cursors struct {
	ToHost   int64 `json:"to_host"`
	ToLaptop int64 `json:"to_laptop"`
}

// remoteChat shows the mail waiting for this laptop, then mails each line read
// from stdin to room and checks for replies every tick, until stdin ends.
func remoteChat(ctx context.Context, c *bucket.Caller, path, room, as string, every time.Duration, stdin io.Reader, stdout io.Writer) error {
	var cur cursors
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &cur); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var mu sync.Mutex // guards cur and the file
	save := func() error {
		raw, _ := json.Marshal(cur)
		return os.WriteFile(path, raw, 0o600)
	}
	check := func() error {
		mu.Lock()
		defer mu.Unlock()
		next, err := mail.Receive(ctx, c, mail.ToLaptop, cur.ToLaptop, func(_ int64, m mail.Message) error {
			fmt.Fprintf(stdout, "[%s] #%s %s: %s\n", m.Time.Local().Format("15:04"), m.Room, m.Author, m.Text)
			return nil
		})
		cur.ToLaptop = next
		if serr := save(); err == nil {
			err = serr
		}
		return err
	}
	if err := check(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				check() // a failed check is retried on the next tick
			case <-ctx.Done():
				return
			}
		}
	})
	defer func() { cancel(); wg.Wait() }()

	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if sc.Text() == "" {
			continue
		}
		mu.Lock()
		seq, err := mail.Send(ctx, c, mail.ToHost, cur.ToHost, mail.Message{Room: room, Author: as, Text: sc.Text()})
		if err == nil {
			cur.ToHost = seq + 1
			err = save()
		}
		mu.Unlock()
		if err != nil {
			return err
		}
	}
	return sc.Err()
}
