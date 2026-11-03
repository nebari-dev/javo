"""Sales explorer: a sample Streamlit app. Edit this file and the app reloads."""

import altair as alt
import numpy as np
import pandas as pd
import streamlit as st

st.set_page_config(page_title="Sales explorer", layout="wide")
st.title("Sales explorer")
st.caption("Made-up numbers, generated when the app starts. Change this file to make it yours.")

REGIONS = ["North", "South", "East", "West"]


@st.cache_data
def make_sales() -> pd.DataFrame:
    rng = np.random.default_rng(7)
    months = pd.date_range("2025-01-01", periods=24, freq="MS")
    rows = []
    for i, region in enumerate(REGIONS):
        base = 100 + 25 * i
        trend = np.linspace(0, 40, len(months))
        noise = rng.normal(0, 8, len(months))
        for month, value in zip(months, base + trend + noise):
            rows.append({"month": month, "region": region, "sales": round(float(value), 1)})
    return pd.DataFrame(rows)


sales = make_sales()

left, right = st.columns(2)
region = left.selectbox("Region", ["All"] + REGIONS)
months = right.slider("Months to show", min_value=3, max_value=24, value=12)

shown = sales if region == "All" else sales[sales["region"] == region]
last = sales["month"].drop_duplicates().sort_values().tail(months)
shown = shown[shown["month"].isin(last)]

chart = (
    alt.Chart(shown)
    .mark_line(point=True)
    .encode(
        x=alt.X("month:T", title="Month"),
        y=alt.Y("sales:Q", title="Sales"),
        color=alt.Color("region:N", title="Region"),
        tooltip=["month:T", "region:N", "sales:Q"],
    )
)
st.altair_chart(chart, use_container_width=True)
st.metric("Total sales", f"{shown['sales'].sum():,.0f}")
