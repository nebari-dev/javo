"""A FastAPI app: a small inventory API with interactive docs at /docs."""
import html
from datetime import datetime, timezone
from string import Template

from fastapi import FastAPI, HTTPException, Request
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
def home(request: Request):
    # Relative links keep working under the gateway's URL prefix, which
    # uvicorn --root-path puts in root_path.
    root_path = request.scope.get("root_path", "")
    path = request.url.path
    if not path.startswith(root_path):
        path = root_path + path
    return HOME.substitute(
        time=html.escape(datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")),
        path=html.escape(path),
    )


HOME = Template("""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Your FastAPI app is running</title>
<!-- The page uses IBM Plex Sans and Fira Code when the viewer has them
     installed and system fonts otherwise: no web fonts are fetched, so it
     also works where the app has no internet access. -->
<style>
  :root {
    --bg: #f6f7f9; --card: #ffffff; --text: #16181d; --muted: #5b6270;
    --line: #e3e6eb; --code: #f0f2f5; --link: #2357c6;
    --brand: #009688; --logo: var(--brand);
    color-scheme: light dark;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0f1115; --card: #171a21; --text: #e8eaee; --muted: #9aa3b2;
      --line: #272c36; --code: #1f232c; --link: #8ab0ff;
      --logo: color-mix(in srgb, var(--brand), #fff 25%);
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px 16px;
    background: var(--bg); color: var(--text);
    font: 16px/1.6 "IBM Plex Sans", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  }
  main {
    width: 100%; max-width: 34rem; padding: 40px;
    background: var(--card); border: 1px solid var(--line); border-radius: 16px;
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.04), 0 8px 24px rgb(0 0 0 / 0.04);
  }
  .logo {
    width: 56px; height: 56px; display: grid; place-items: center; border-radius: 14px;
    background: color-mix(in srgb, var(--brand) 12%, transparent);
  }
  .logo svg { width: 32px; height: 32px; fill: var(--logo); }
  h1 { margin: 24px 0 8px; font-size: 1.6rem; font-weight: 600; line-height: 1.25; letter-spacing: -0.01em; }
  p { margin: 0; color: var(--muted); }
  code, dd { font-family: "Fira Code", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.875em; }
  code { padding: 0.1em 0.35em; border-radius: 6px; background: var(--code); color: var(--text); overflow-wrap: anywhere; }
  dl { margin: 28px 0 0; padding-top: 20px; border-top: 1px solid var(--line); display: grid; grid-template-columns: auto 1fr; gap: 6px 20px; font-size: 0.875rem; }
  dt { color: var(--muted); }
  dd { margin: 0; overflow-wrap: anywhere; }
  footer { margin-top: 28px; display: flex; align-items: center; gap: 8px; font-size: 0.875rem; color: var(--muted); }
  footer svg { width: 16px; height: 16px; fill: currentColor; }
  a { color: var(--link); text-decoration: none; }
  a:hover { text-decoration: underline; }
  nav { margin-top: 20px; display: flex; flex-wrap: wrap; gap: 8px; }
  nav a { padding: 6px 12px; border: 1px solid var(--line); border-radius: 8px; font-size: 0.875rem; }
  nav a:hover { text-decoration: none; border-color: var(--link); }
  @media (max-width: 480px) { main { padding: 28px 24px; } }
</style>
</head>
<body>
<main>
  <!-- FastAPI logo: Simple Icons 16.35.0 (icons/fastapi.svg), CC0-1.0;
       the logo is from the MIT-licensed FastAPI repository (docs/en/docs/img/icon-white.svg). -->
  <div class="logo"><svg viewBox="0 0 24 24" role="img" aria-label="FastAPI"><path d="M12 .0387C5.3729.0384.0003 5.3931 0 11.9988c-.001 6.6066 5.372 11.9628 12 11.9625 6.628.0003 12.001-5.3559 12-11.9625-.0003-6.6057-5.3729-11.9604-12-11.96m-.829 5.4153h7.55l-7.5805 5.3284h5.1828L5.279 18.5436q2.9466-6.5444 5.892-13.0896"/></svg></div>
  <h1>Your FastAPI app is running</h1>
  <p>This is a sample app deployed with Javo. Edit <code>samples/fastapi/sample.py</code> in your home folder and restart the app to change it.</p>
  <nav>
    <a href="docs">Interactive docs</a>
    <a href="openapi.json">OpenAPI schema</a>
    <a href="items">GET /items</a>
  </nav>
  <dl>
    <dt>Server time</dt><dd>$time</dd>
    <dt>Path</dt><dd>$path</dd>
  </dl>
  <footer>
    <svg viewBox="0 0 100 100" aria-hidden="true"><path fill-rule="evenodd" d="M22 0H78A22 22 0 0 1 100 22V78A22 22 0 0 1 78 100H22A22 22 0 0 1 0 78V22A22 22 0 0 1 22 0ZM28.8 80L49.2 20H63.2L42.8 80ZM54.8 80L68.4 40H82.4L68.8 80Z"/></svg>
    <span>Deployed with Javo. <a href="https://javo.nebari.dev">Read the docs</a></span>
  </footer>
</main>
</body>
</html>
""")


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
