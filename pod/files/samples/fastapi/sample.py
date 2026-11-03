"""A FastAPI app: a small inventory API with interactive docs at /docs."""
from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse
from pydantic import BaseModel, Field

# The URL prefix comes from uvicorn --root-path (set by the template), which
# also points /docs at the right schema URL.
app = FastAPI(title="Inventory", description="A small example API.")


class Item(BaseModel):
    name: str = Field(min_length=1, max_length=50)
    price: float = Field(gt=0)
    quantity: int = Field(default=0, ge=0)


items: dict[int, Item] = {
    1: Item(name="Notebook", price=3.5, quantity=12),
    2: Item(name="Pencil", price=0.8, quantity=40),
}


@app.get("/", response_class=HTMLResponse, include_in_schema=False)
def home():
    # Relative links keep working under the gateway's URL prefix.
    return """<!doctype html><meta charset=utf-8><title>Inventory API</title>
<h1>Inventory API</h1>
<p>Try the endpoints in the <a href="docs">interactive docs</a>,
or read the <a href="openapi.json">OpenAPI schema</a>.</p>
<p>Start with <a href="items">GET /items</a>.</p>"""


@app.get("/items")
def list_items() -> dict[int, Item]:
    return items


@app.get("/items/{item_id}")
def get_item(item_id: int) -> Item:
    if item_id not in items:
        raise HTTPException(status_code=404, detail="No such item")
    return items[item_id]


@app.post("/items", status_code=201)
def add_item(item: Item) -> dict[str, int]:
    item_id = max(items, default=0) + 1
    items[item_id] = item
    return {"id": item_id}
