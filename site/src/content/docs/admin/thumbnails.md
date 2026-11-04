---
title: App thumbnails
description: Optional screenshots on app cards.
sidebar:
  order: 11
---

With thumbnails on, an app's owner (or a platform admin) can upload a PNG or JPEG screenshot, shown on the app's card to everyone who may open the app. Off, cards show the logo of the app's kind.

```yaml
config:
  thumbnails:
    enabled: true
```

Uploads are checked by their bytes (not their name), encoded again, shrunk and stored in the database. An upload may be at most 4 MB and 16 megapixels. The gateway decodes at most `config.api.limits.thumbnailDecodes` uploads at once (4 by default); one beyond that is asked to try again in a moment. Removing a thumbnail brings the logo back.
