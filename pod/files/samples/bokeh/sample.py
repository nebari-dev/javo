"""A bokeh serve app: two sliders reshape a sine wave."""
import math

from bokeh.io import curdoc
from bokeh.layouts import column
from bokeh.models import ColumnDataSource, Slider
from bokeh.plotting import figure

xs = [i / 25 for i in range(160)]
source = ColumnDataSource(data={"x": xs, "y": [math.sin(x) for x in xs]})

amplitude = Slider(title="Amplitude", start=0.1, end=5, value=1, step=0.1)
frequency = Slider(title="Frequency", start=0.1, end=5, value=1, step=0.1)


def update(attr, old, new):
    source.data = {"x": xs, "y": [amplitude.value * math.sin(frequency.value * x) for x in xs]}


for s in (amplitude, frequency):
    s.on_change("value", update)

fig = figure(height=320, sizing_mode="stretch_width", title="Sine wave", y_range=(-5, 5))
fig.line("x", "y", source=source, line_width=2)

curdoc().add_root(column(amplitude, frequency, fig, sizing_mode="stretch_width"))
curdoc().title = "Sine wave"
