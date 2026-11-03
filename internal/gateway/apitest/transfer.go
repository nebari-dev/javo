package apitest

import (
	"encoding/json"

	"github.com/nebari-dev/javo/internal/model"
)

type TransferJSON struct {
	Rows []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Outcome     string `json:"outcome"`
		Message     string `json:"message"`
	} `json:"rows"`
}

func ImportBody(yaml, onExists string) string {
	b, _ := json.Marshal(map[string]string{"yaml": yaml, "onExists": onExists})
	return string(b)
}

// WithFile gives a streamlit app the file it runs, as every real one has.
func WithFile(a *model.App) *model.App {
	a.File = "app.py"
	return a
}

func StreamlitApp(owner, display string) string {
	return "---\napiVersion: javo.nebari.dev/v1alpha1\nkind: App\nmetadata: {name: x}\nspec: {displayName: " + display +
		", template: streamlit, file: app.py}\n"
}
