import marimo

app = marimo.App()


@app.cell
def _():
    total = sum(range(10))
    print(f"total {total}")
    return


if __name__ == "__main__":
    app.run()
