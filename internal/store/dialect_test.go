package store_test

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/dialect"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The one-query-set check: the same sqlc output (generated with the postgresql
// engine) must run on whichever engine JAVO_TEST_DB names. make test-db runs
// this on both.
func TestOneQuerySetRunsOnBothEngines(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, storetest.Config(t))
	db := s.DB()
	if _, err := db.ExecContext(ctx, dialect.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	q := dialect.New(db)

	created := dbtime.From(time.Date(2026, 10, 31, 12, 0, 0, 123_000_000, time.UTC))
	alpha, err := q.InsertThing(ctx, dialect.InsertThingParams{
		ID: "a", Name: "alpha", Flag: true, Count: 1, Doc: `{"k":[1,2]}`,
		Note: sql.NullString{String: "alpha", Valid: true}, CreatedAt: created,
	})
	if err != nil {
		t.Fatalf("INSERT RETURNING: %v", err)
	}
	if alpha.ID != "a" || !alpha.Flag || alpha.Count != 1 || alpha.Doc != `{"k":[1,2]}` {
		t.Fatalf("RETURNING row = %+v", alpha)
	}
	if !alpha.CreatedAt.Time().Equal(created.Time()) {
		t.Fatalf("created_at round trip = %v, want %v", alpha.CreatedAt.Time(), created.Time())
	}
	if alpha.SeenAt.Valid {
		t.Fatalf("NULL seen_at scanned as valid: %+v", alpha.SeenAt)
	}

	if _, err := q.InsertThing(ctx, dialect.InsertThingParams{ID: "b", Name: "beta", Count: 5, Doc: "{}", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertThing(ctx, dialect.InsertThingParams{ID: "c", Name: "alps", Count: 1, Doc: "{}", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}

	seen := dbtime.NullTime{Time: created, Valid: true}
	up, err := q.UpsertThing(ctx, dialect.UpsertThingParams{
		ID: "a", Name: "alpha2", Flag: false, Count: 2, Doc: "{}", CreatedAt: created, SeenAt: seen,
	})
	if err != nil {
		t.Fatalf("ON CONFLICT DO UPDATE: %v", err)
	}
	if up.Name != "alpha2" || up.Count != 3 || !up.Flag || !up.SeenAt.Valid {
		t.Fatalf("upsert row = %+v (want name alpha2, count 3, flag kept, seen_at set)", up)
	}

	// One parameter used twice: matches the row whose name or note is "alpha2"
	// or "alpha"; only the note "alpha" is left after the upsert renamed it.
	byEither, err := q.ThingsByNameOrNote(ctx, "alpha2")
	if err != nil || len(byEither) != 1 || byEither[0].ID != "a" {
		t.Fatalf("reused parameter: rows=%v err=%v", byEither, err)
	}
	byNote, err := q.ThingsByNameOrNote(ctx, "alpha")
	if err != nil || len(byNote) != 1 || byNote[0].Note.String != "alpha" {
		t.Fatalf("reused parameter on note: rows=%v err=%v", byNote, err)
	}
	if got, _ := q.ThingsByNameOrNote(ctx, "beta"); len(got) != 1 || got[0].Note.Valid {
		t.Fatalf("NULL note scan: %+v", got)
	}

	prefixed, err := q.ThingsWithNamePrefix(ctx, "al")
	if err != nil || len(prefixed) != 2 || prefixed[0].ID != "a" || prefixed[1].ID != "c" {
		t.Fatalf("LIKE prefix: rows=%v err=%v", prefixed, err)
	}

	flagged, err := q.CountThingsFlagged(ctx, true)
	if err != nil || flagged != 1 {
		t.Fatalf("boolean filter: n=%d err=%v", flagged, err)
	}
}

var placeholder = regexp.MustCompile(`\$(\d+)`)

// generatedQueries returns every SQL string constant in the sqlc output next
// to this package, keyed by "dir/const".
func generatedQueries(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*/*.sql.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no generated query files found: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					sqlText, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out[filepath.Dir(f)+"/"+vs.Names[i].Name] = sqlText
				}
			}
		}
	}
	return out
}

// SQLite numbers "$N" parameters by first appearance, so a query whose
// placeholders first appear out of order would bind the wrong arguments there.
func TestPlaceholdersFirstAppearInAscendingOrder(t *testing.T) {
	for name, text := range generatedQueries(t) {
		next := 1
		for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
			n, _ := strconv.Atoi(m[1])
			switch {
			case n == next:
				next++
			case n > next:
				t.Errorf("%s: $%d appears before $%d", name, n, next)
			}
		}
	}
}

// Syntax SQLite does not have.
func TestQueriesAvoidPostgresOnlySyntax(t *testing.T) {
	banned := regexp.MustCompile(`::|(?i)\bILIKE\b|(?i)\bANY\s*\(|(?i)\bUNNEST\b|->>?|@>|(?i)\bNOW\s*\(`)
	for name, text := range generatedQueries(t) {
		if m := banned.FindString(text); m != "" {
			t.Errorf("%s uses %q, which SQLite lacks", name, strings.TrimSpace(m))
		}
	}
}

// Time columns hold unix milliseconds: every column named *_at must be
// generated as dbtime.Time or dbtime.NullTime, never a bare integer.
func TestTimeColumnsUseDBTime(t *testing.T) {
	files, _ := filepath.Glob("*/models.go")
	if len(files) == 0 {
		t.Fatal("no generated models found")
	}
	for _, f := range files {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok {
				return true
			}
			for _, name := range field.Names {
				if !strings.HasSuffix(name.Name, "At") {
					continue
				}
				sel, ok := field.Type.(*ast.SelectorExpr)
				if !ok || sel.X.(*ast.Ident).Name != "dbtime" {
					t.Errorf("%s: field %s is not a dbtime type", f, name.Name)
				}
			}
			return true
		})
	}
}
