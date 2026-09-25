package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/workspace"
)

// TestRealMiseInstallsThisRepoStack runs the real mise against a clone of
// this repo. It downloads Go, so it runs only with GARAGE_TEST_MISE=1.
func TestRealMiseInstallsThisRepoStack(t *testing.T) {
	if os.Getenv("GARAGE_TEST_MISE") == "" {
		t.Skip("set GARAGE_TEST_MISE=1 to run the real mise")
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ws, err := workspace.Open(context.Background(), workspace.Config{
		Name: "agentgarage", Remote: strings.TrimSpace(string(repo)), Root: root,
		Identity: workspace.Identity{Name: "garage-dev", Email: "dev@garage.invalid"},
		Mise:     mise,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := ws.Start(context.Background(), "real-mise")
	if err != nil {
		t.Fatal(err)
	}

	// The module cache is read-only, so TempDir can't remove it by itself.
	t.Cleanup(func() { sh(t, task.Sandbox(), "go clean -modcache") })

	res := sh(t, task.Sandbox(), "go version && go env GOROOT")
	tools := filepath.Join(root, "agentgarage", "tools")
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "go1.26") || !strings.Contains(res.Stdout, "agentgarage/tools/installs/go/") {
		t.Errorf("want Go 1.26 from %s:\n%s%s", tools, res.Stdout, res.Stderr)
	}
	installed, _ := os.ReadDir(filepath.Join(tools, "installs"))
	for _, e := range installed {
		if e.Name() != "go" {
			t.Errorf("installed %s, which this repo's mise.toml does not pin", e.Name())
		}
	}
	if res := sh(t, task.Sandbox(), "go build -o /dev/null ./cmd/garage"); res.ExitCode != 0 {
		t.Errorf("go build: %s%s", res.Stdout, res.Stderr)
	}
}
