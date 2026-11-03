"""A Dash app: a scatter of the iris dataset bundled with plotly, filtered by a dropdown."""
import plotly.express as px
from dash import Dash, Input, Output, dcc, html

iris = px.data.iris()
species = sorted(iris["species"].unique())

app = Dash(__name__, title="Iris explorer")
app.layout = html.Div(
    [
        html.H1("Iris explorer"),
        dcc.Dropdown(species, species, multi=True, id="species"),
        dcc.Graph(id="scatter"),
    ]
)


@app.callback(Output("scatter", "figure"), Input("species", "value"))
def scatter(chosen):
    rows = iris[iris["species"].isin(chosen or [])]
    return px.scatter(rows, x="sepal_width", y="sepal_length", color="species")


if __name__ == "__main__":
    # Address, port and URL prefix come from HOST, PORT and
    # DASH_URL_BASE_PATHNAME, set by the template.
    app.run()
