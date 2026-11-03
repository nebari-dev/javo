import marimo

__generated_with = "0.25.1"
app = marimo.App()


@app.cell
def _():
    import marimo as mo
    return (mo,)


@app.cell
def _(mo):
    mo.md("""
    # Compound interest

    Move the sliders: the table below recomputes on its own.
    """)
    return


@app.cell
def _(mo):
    rate = mo.ui.slider(1, 15, value=5, label="Yearly rate (%)")
    years = mo.ui.slider(1, 30, value=10, label="Years")
    mo.vstack([rate, years])
    return rate, years


@app.cell
def _(mo, rate, years):
    rows = []
    balance = 1000.0
    for year in range(1, years.value + 1):
        balance *= 1 + rate.value / 100
        rows.append({"year": year, "balance": round(balance, 2)})
    mo.ui.table(rows, selection=None)
    return


if __name__ == "__main__":
    app.run()
