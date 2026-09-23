package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/maikdotfi/agentgarage/chatroom"
)

// chat shows a room and follows it, posting each line read from stdin, until
// stdin ends.
func chat(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	room := fs.String("room", "garage", "room to talk in")
	as := fs.String("as", os.Getenv("USER"), "who you are in the room")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sock := filepath.Join(garageHome(), socketFile)
	c := socketClient(sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgs, err := c.read(ctx, *room, 0, "")
	if err != nil {
		fmt.Fprintf(stderr, "garage chat: can't reach garage serve at %s: %v\n", sock, err)
		return 1
	}
	var last int64
	for _, m := range msgs {
		printMessage(stdout, m)
		last = m.ID
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for ctx.Err() == nil {
			msgs, err := c.read(ctx, *room, last, "30s")
			if err != nil {
				return
			}
			for _, m := range msgs {
				printMessage(stdout, m)
				last = m.ID
			}
		}
	})

	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if sc.Text() == "" {
			continue
		}
		if err := c.post(ctx, *room, *as, sc.Text()); err != nil {
			fmt.Fprintln(stderr, "garage chat:", err)
		}
	}
	cancel()
	wg.Wait()
	return 0
}

func printMessage(w io.Writer, m chatroom.Message) {
	fmt.Fprintf(w, "[%s] %s: %s\n", m.Time.Local().Format("15:04"), m.Author, m.Text)
}

// chatClient talks to the chatroom API on the unix socket.
type chatClient struct{ http *http.Client }

func socketClient(sock string) chatClient {
	return chatClient{&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}}
}

func (c chatClient) read(ctx context.Context, room string, after int64, wait string) ([]chatroom.Message, error) {
	u := fmt.Sprintf("http://garage/rooms/%s/messages?after=%d&wait=%s", url.PathEscape(room), after, wait)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read %s: %s", room, resp.Status)
	}
	var msgs []chatroom.Message
	return msgs, json.NewDecoder(resp.Body).Decode(&msgs)
}

func (c chatClient) post(ctx context.Context, room, author, text string) error {
	u := "http://garage/rooms/" + url.PathEscape(room) + "/messages"
	form := url.Values{"author": {author}, "text": {text}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("post to %s: %s", room, resp.Status)
	}
	return nil
}
