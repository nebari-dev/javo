---
title: Branding
description: Your own product name, logo, favicon and accent colour.
sidebar:
  order: 11.5
---

An install can show its own name, logo, favicon and accent colour instead of Javo's. Everyone sees them, the sign-in page too. They are display only: Kubernetes objects, labels, cookies and environment variables keep the name `javo`.

```yaml
branding:
  name: Acme Lab # sidebar, sign-in page and browser tab title
  accent: "#0a7d55" # #rrggbb or #rgb, used in both themes
  logo:
    light:
      svg: | # the SVG file's text
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 30">...</svg>
    dark:
      url: https://cdn.example.com/acme-logo-dark.svg # optional, for the dark theme
  favicon:
    base64: iVBORw0KGgoAAAANSUhEUgAA... # a PNG, base64
```

Each image takes one of:

- `url`: an https address the browser loads the image from. The UI allows images from that host and from no other.
- `svg`: the text of an SVG file. Pass a file with `helm upgrade ... --set-file branding.logo.light.svg=logo.svg`.
- `base64`: a PNG, JPEG, GIF, WebP or ICO file in base64, such as `base64 -w0 favicon.png`.

`svg` and `base64` images go into the ConfigMap `javo-branding`; the gateway serves them at `/branding/logo-light`, `/branding/logo-dark` and `/branding/favicon`. Each file may be at most 256 KiB, and its type is told by its bytes: the gateway does not start with a file that is not one of those images. An SVG is served with a Content-Security-Policy that stops it from running script or loading anything.

The logo is drawn 30 pixels high at the top left of the sidebar and on the sign-in page, next to the name. Without `dark`, the light logo shows in both themes. Leave a value empty to keep Javo's own. The UI reads the branding from `GET /api/branding`, which needs no sign-in.
