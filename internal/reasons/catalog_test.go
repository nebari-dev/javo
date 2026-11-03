package reasons

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// codeConstants returns every constant of type Code declared in this
// package's source, so a new code cannot be added without a catalog entry.
func codeConstants(t *testing.T) []Code {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []Code
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, sp := range g.Specs {
					vs := sp.(*ast.ValueSpec)
					if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Code" {
						continue
					}
					for i := range vs.Names {
						lit := vs.Values[i].(*ast.BasicLit)
						out = append(out, Code(strings.Trim(lit.Value, `"`)))
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no Code constants found")
	}
	return out
}

func TestEveryCodeHasACatalogEntry(t *testing.T) {
	for _, code := range codeConstants(t) {
		if _, ok := Lookup(code); !ok {
			t.Errorf("%s missing from catalog", code)
		}
	}
}

func TestCatalogEntriesHaveCopy(t *testing.T) {
	for _, r := range All() {
		if r.Title == "" || r.What == "" || r.Todo == "" {
			t.Errorf("%s has empty copy: %+v", r.Code, r)
		}
		if r.Blamed == "" {
			t.Errorf("%s has no blamed party", r.Code)
		}
	}
}

func TestCatalogCopyHasNoDashes(t *testing.T) {
	for _, r := range All() {
		for _, s := range []string{r.Title, r.What, r.Todo, r.Blame, r.Who} {
			if strings.ContainsAny(s, "\u2014\u2013") {
				t.Errorf("%s contains an em or en dash: %q", r.Code, s)
			}
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup(Code("Nope")); ok {
		t.Error("unknown code found")
	}
}

func TestAllHasNoDuplicates(t *testing.T) {
	seen := map[Code]bool{}
	for _, r := range All() {
		if seen[r.Code] {
			t.Errorf("duplicate %s", r.Code)
		}
		seen[r.Code] = true
	}
}

// The buttons the app status page offers. Copy that tells the user to press
// a button must name one of these, or the advice cannot be followed.
var uiActions = []string{"Open", "Start", "Stop", "Restart", "Delete"}

func TestTodoNamesOnlyButtonsThatExist(t *testing.T) {
	press := regexp.MustCompile(`\bpress (\w+)`)
	for _, r := range All() {
		for _, m := range press.FindAllStringSubmatch(r.Todo, -1) {
			if !slices.Contains(uiActions, m[1]) {
				t.Errorf("%s says %q but the page has no %s button", r.Code, m[0], m[1])
			}
		}
	}
	for _, code := range []Code{AppCrashed, OutOfMemory} {
		r, _ := Lookup(code)
		if !strings.Contains(r.Todo, "press Restart") {
			t.Errorf("%s todo does not point at Restart: %q", code, r.Todo)
		}
	}
}

func TestNetworkPolicyNotEnforcedWho(t *testing.T) {
	r, _ := Lookup(NetworkPolicyNotEnforced)
	if r.Who != "A cluster admin can enable a network plugin that enforces NetworkPolicy." {
		t.Errorf("Who = %q", r.Who)
	}
}
