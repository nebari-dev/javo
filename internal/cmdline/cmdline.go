// Package cmdline splits a command line a person typed into the words a
// program receives, with the quoting rules of a POSIX shell
// (mvdan.cc/sh/v3). No shell ever runs the command, so whatever only a
// shell could do (pipes, redirects, several commands, substitutions,
// variables) is refused rather than passed on as text.
package cmdline

import (
	"errors"
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// ErrVariable is returned for a $NAME, ${NAME} or leading ~, which a shell
// would fill in and a program would get as text.
var ErrVariable = errors.New("variables are not filled in")

// SyntaxError is a command line that cannot be read as words. Detail is the
// parser's description, with the line and column.
type SyntaxError struct{ Detail string }

func (e *SyntaxError) Error() string { return "command line: " + e.Detail }

// Split returns the words of line. A blank line has no words.
func Split(line string) ([]string, error) {
	p := syntax.NewParser()
	var words []*syntax.Word
	for w, err := range p.WordsSeq(strings.NewReader(line)) {
		if err != nil {
			return nil, &SyntaxError{Detail: err.Error()}
		}
		words = append(words, w)
	}
	for _, w := range words {
		if err := check(w); err != nil {
			return nil, err
		}
	}
	// An empty environment: nothing is looked up from the process running this.
	out, err := expand.Fields(&expand.Config{Env: expand.ListEnviron()}, words...)
	if err != nil {
		return nil, &SyntaxError{Detail: err.Error()}
	}
	return out, nil
}

// check refuses the parts of a word only a shell could fill in.
func check(w *syntax.Word) error {
	if len(w.Parts) > 0 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(lit.Value, "~") {
			return ErrVariable
		}
	}
	var err error
	syntax.Walk(w, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ParamExp:
			err = ErrVariable
		case *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst:
			err = &SyntaxError{Detail: fmt.Sprintf("%s: substitutions are not supported", n.Pos())}
		}
		return err == nil
	})
	return err
}

// Join writes words as a line that Split reads back as the same words,
// quoting a word only where it needs it.
func Join(words []string) (string, error) {
	out := make([]string, len(words))
	for i, w := range words {
		q, err := syntax.Quote(w, syntax.LangPOSIX)
		if err != nil {
			return "", err
		}
		out[i] = q
	}
	return strings.Join(out, " "), nil
}
