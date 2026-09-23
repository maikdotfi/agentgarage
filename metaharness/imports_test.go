package metaharness_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoGarageImports keeps the library independent of its caller: nothing
// under metaharness/ may import another agentgarage package.
func TestNoGarageImports(t *testing.T) {
	const garage = "github.com/maikdotfi/agentgarage"
	const self = garage + "/metaharness"

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "." {
				return nil
			}
			// Nested modules, testdata and hidden dirs are not part of the library.
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil ||
				d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			inGarage := p == garage || strings.HasPrefix(p, garage+"/")
			inSelf := p == self || strings.HasPrefix(p, self+"/")
			if inGarage && !inSelf {
				t.Errorf("%s imports %s; metaharness must not import the garage", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
