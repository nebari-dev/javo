# Using the Javo API from this app

This app runs with an API token of the person who owns it, so you can list
templates and create, start, stop and delete their apps. The token can do
anything they can do in the Javo web UI, except open apps (an app's URL needs a
browser sign-in) and manage tokens. If the app was given read access only,
the token may only GET, and any other method answers 403. It is revoked when
this app is deleted or its API access changes.

- `$JAVO_URL` is the Javo web address, for example https://javo.example.com.
- `$JAVO_TOKEN` is the token. Send it on every request and never print it,
  write it to a file or commit it.

Call it with `javo-api [METHOD] PATH [JSON]` (GET by default). It sends the
token, prints the answer as JSON, and exits non-zero on an error answer.

The full API is described by the OpenAPI document at
`$JAVO_URL/api/openapi.json` (no token needed). Read it when a call below is not
enough. Errors are JSON problems: `title` says what went wrong and `errors`
names the field.

## Common calls

```sh
javo-api /api/me                     # who you act as
javo-api /api/templates              # templates: .items[].name, kind, userCommand, userImage, fileExtensions
javo-api /api/sizes                  # sizes you may pick
javo-api /api/workspaces             # the owner's apps

# Create an app from a template. "name" in the answer is the app's short name.
javo-api POST /api/workspaces '{"displayName":"Sales dashboard","template":"streamlit","file":"sales/app.py"}'

# A template with userCommand Optional or Required runs a command you give
# (no shell; {{.Port}}, {{.BaseURL}} and {{.Home}} are filled in).
javo-api POST /api/workspaces '{"displayName":"My API","template":"custom","command":"python -m http.server {{.Port}}"}'

# A template with userImage runs a container image you name, on its port.
javo-api POST /api/workspaces '{"displayName":"Demo","template":"container","image":"registry.example.com/team/app:1.0","port":8080}'

javo-api /api/workspaces/NAME        # .phase (Starting, Running, Stopped, Failed), .url, .steps[]
javo-api /api/workspaces/NAME/logs   # the app's log
javo-api POST /api/workspaces/NAME/stop
javo-api POST /api/workspaces/NAME/start
javo-api DELETE /api/workspaces/NAME # the home folder is kept
```

Poll `GET /api/workspaces/NAME` until `phase` is `Running`, then give the
person `url`. When `phase` is `Failed`, the failing step in `steps` has
`title`, `what` and `todo` that explain it; report those words.

Files are in the owner's home folder, which is your home folder here: write an
app's code there before creating an app that runs it (`file` is relative to the
home folder).
