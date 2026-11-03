package workspace

import (
	"testing"

	"github.com/nebari-dev/javo/internal/model"
)

// The file an app opens, as a path inside its container: under the home
// folder, or under the folder of the clone for an app that runs from Git.
func TestFilePathIsTheAbsolutePathInTheApp(t *testing.T) {
	for _, c := range []struct {
		name string
		app  model.App
		want string
	}{
		{"home", model.App{File: "samples/streamlit/app.py"}, "/home/user/samples/streamlit/app.py"},
		{"repo root", model.App{File: "app.py", Source: &model.Source{}}, "/srv/src/current/app.py"},
		{"repo folder", model.App{File: "app.py", Source: &model.Source{Path: "web"}}, "/srv/src/current/web/app.py"},
		{"no file", model.App{}, ""},
	} {
		if got := FilePath(&c.app); got != c.want {
			t.Errorf("%s: FilePath = %q, want %q", c.name, got, c.want)
		}
	}
}
