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
  serve     run agents and the chatroom (host)
  door      run the tunnel, SSH access and the mail poller (host)
  remote    the laptop side: UI, mail, the door
  backup    snapshot every database to the bucket
  restore   bring databases back from the bucket
  chat      talk to the chatroom over the local socket
`

// commands maps each subcommand to its entry point, which gets the remaining
// arguments and returns an exit code.
var commands = map[string]func(args []string, stdout, stderr io.Writer) int{
	"serve":   notYet("serve"),
	"door":    notYet("door"),
	"remote":  notYet("remote"),
	"backup":  notYet("backup"),
	"restore": notYet("restore"),
	"chat":    notYet("chat"),
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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
	return cmd(rest, stdout, stderr)
}

func notYet(name string) func([]string, io.Writer, io.Writer) int {
	return func(_ []string, _, stderr io.Writer) int {
		fmt.Fprintf(stderr, "garage %s: not implemented yet\n", name)
		return 1
	}
}
