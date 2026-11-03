"""A Panel app: a slider bound with pn.bind to a Bokeh line plot."""
import math

import panel as pn
from bokeh.plotting import figure

pn.extension()


def plot(frequency):
    xs = [i / 20 for i in range(200)]
    ys = [math.sin(frequency * x) * math.exp(-x / 5) for x in xs]
    fig = figure(height=320, sizing_mode="stretch_width", title=f"Damped wave, frequency {frequency}")
    fig.line(xs, ys, line_width=2)
    return fig


frequency = pn.widgets.IntSlider(name="Frequency", start=1, end=10, value=3)

pn.Column(
    "# Damped wave",
    "Move the slider to change the frequency.",
    frequency,
    pn.bind(plot, frequency),
).servable(title="Damped wave")
