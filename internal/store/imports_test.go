package store_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// internal/store is the only code that touches SQL: nothing else imports
// database/sql, a database driver, goose or sqlc output.
func TestOnlyStoreImportsSQL(t *testing.T) {
	root := filepath.Join("..", "..")
	storeDir := filepath.Join(root, "internal", "store")
	forbidden := []string{
		"database/sql",
		"github.com/jackc/pgx",
		"modernc.org/sqlite",
		"github.com/pressly/goose",
		"github.com/nebari-dev/javo/internal/store/sqlcgen",
		"github.com/nebari-dev/javo/internal/store/dialect",
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "bin", ".dev":
				return filepath.SkipDir
			}
			if path == storeDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if p == bad || strings.HasPrefix(p, bad+"/") {
					t.Errorf("%s imports %s; only internal/store may", path, p)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
