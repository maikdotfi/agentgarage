package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, strings.NewReader(""), &out, &errOut)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	for _, cmd := range []string{"serve", "door", "remote", "backup", "restore", "chat"} {
		if !strings.Contains(errOut.String(), cmd) {
			t.Errorf("usage does not mention %q:\n%s", cmd, errOut.String())
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"frobnicate"}, strings.NewReader(""), &out, &errOut)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), `unknown command "frobnicate"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", errOut.String())
	}
}

func TestHelpPrintsUsageToStdout(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"help"}, strings.NewReader(""), &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "serve") {
		t.Errorf("stdout = %q, want usage", out.String())
	}
}

func TestCommandsNotYetBuiltSayYet(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"door"}, strings.NewReader(""), &out, &errOut)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "door: not implemented yet") {
		t.Errorf("stderr = %q", errOut.String())
	}
}
