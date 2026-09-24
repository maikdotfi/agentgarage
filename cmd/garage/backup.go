package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/maikdotfi/agentgarage/hosting"
)

// backup asks garage serve, which owns the databases, to snapshot each of
// them to the bucket. A systemd timer runs it daily.
func backup(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: garage backup")
		return 2
	}
	sock := filepath.Join(garageHome(), socketFile)
	req, _ := http.NewRequest(http.MethodPost, "http://garage/backup", nil)
	resp, err := socketClient(sock).http.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "garage backup: can't reach garage serve at %s: %v\n", sock, err)
		return 1
	}
	defer resp.Body.Close()
	out := stdout
	if resp.StatusCode != http.StatusOK {
		out = stderr
		fmt.Fprint(stderr, "garage backup: ")
	}
	io.Copy(out, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// restore brings back the latest snapshot of every database that is missing,
// and never touches one that is there. garage serve does the same when it starts.
func restore(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: garage restore")
		return 2
	}
	b, err := openBucket(keysDir(), nil, serveLimits)
	if err != nil {
		fmt.Fprintln(stderr, "garage restore:", err)
		return 1
	}
	paths := map[string]string{}
	for owner, rel := range databases {
		paths[owner] = filepath.Join(garageHome(), rel)
	}
	restored, err := hosting.Restore(context.Background(), b.Caller("backup"), paths)
	for _, p := range restored {
		fmt.Fprintln(stdout, "restored", p)
	}
	if err != nil {
		fmt.Fprintln(stderr, "garage restore:", err)
		return 1
	}
	if len(restored) == 0 {
		fmt.Fprintln(stdout, "nothing to restore: every database is there, or has no snapshot")
	}
	return 0
}
