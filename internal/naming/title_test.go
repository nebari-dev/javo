package naming

import "testing"

// A request to a coding agent becomes a short sentence-case title: the
// verb and filler in front go, it ends on a word that carries meaning,
// and it never ends cut off mid clause.
func TestPromptTitle(t *testing.T) {
	for in, want := range map[string]string{
		"create a streamlit app that lets me explore stock prices":                         "Streamlit app to explore stock prices",
		"create a Streamlit app that shows a random number":                                "Streamlit app that shows a random number",
		"Build me a dashboard of sales/data.csv by month":                                  "Dashboard of sales/data.csv by month",
		"Please make a FastAPI service for todo items with SQLite":                         "FastAPI service for todo items",
		"Can you write a Jupyter notebook that plots the iris dataset":                     "Jupyter notebook that plots the iris dataset",
		"A Streamlit dashboard of sales/data.csv":                                          "Streamlit dashboard of sales/data.csv",
		"Build a todo app. Use React and keep it simple.":                                  "Todo app",
		"I want you to set up a Panel app which allows users to upload a CSV and chart it": "Panel app to upload a CSV",
		"deploy the app in ~/projects/sales-dashboard please":                              "App in ~/projects/sales-dashboard",
		"make a":                       "Make a",
		"x":                            "X",
		"  build  a   gradio   chat  ": "Gradio chat",
	} {
		if got := PromptTitle(in); got != want {
			t.Errorf("PromptTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
