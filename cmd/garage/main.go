// Command garage is the one binary of the Agent Garage. Each subcommand is one
// role: the host runs serve and door, a human's laptop runs remote.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `usage: garage <command> [arguments]

commands:
  serve     run agents, the chatroom and the chat UI (host)
  door      run the tunnel, SSH access and the mail poller (host)
  remote    the laptop side: config, secrets, mail
  backup    snapshot every database to the bucket
  restore   bring databases back from the bucket
  chat      talk to the chatroom over the local socket
  init      make this machine's keys (-master on the host)
  setup     make this Debian machine a garage host, as root
`

// command is one subcommand: it gets the remaining arguments and returns an
// exit code.
type command func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

var commands = map[string]command{
	"serve":   serve,
	"door":    notYet("door"),
	"remote":  remote,
	"backup":  backup,
	"restore": restore,
	"chat":    chat,
	"init":    initKeys,
	"setup":   setup,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(stderr, "garage: unknown command %q\n\n%s", name, usage)
		return 2
	}
	return cmd(rest, stdin, stdout, stderr)
}

func notYet(name string) command {
	return func(_ []string, _ io.Reader, _, stderr io.Writer) int {
		fmt.Fprintf(stderr, "garage %s: not implemented yet\n", name)
		return 1
	}
}
