# Using the Javo API from this app

This app runs with an API token of the person who owns it.

- `$JAVO_URL` is the Javo web address, for example https://javo.example.com.
- `$JAVO_TOKEN` is the token. Send it on every request and never print it,
  write it to a file or commit it.

Call it with `javo-api [METHOD] PATH [JSON]` (GET by default). It sends the
token, prints the answer as JSON, and exits non-zero on an error answer.
The full API is the OpenAPI document at `$JAVO_URL/api/openapi.json`. Errors
are JSON problems: `title` says what went wrong and `errors` names the field.

What the token may do depends on the app's template:

- Every app token reads everything its owner can see and may start, stop,
  restart and redeploy apps, run jobs, and propose a change for the owner to
  approve (`POST /api/plans`). It cannot write files or create or change apps
  and jobs, and never touches secrets.
- A coding-agent app (the pi template) may also create, change and delete
  the owner's apps and jobs, as long as they hold no secret, API token or Nebi
  token. Anything else (secrets, API access, sharing, or any change to an app
  or job that holds a secret) answers 403: ask the person to do it in the
  browser, or propose it as a plan.

## Common calls

```sh
javo-api /api/me                     # who you act as
javo-api /api/templates              # .items[].name, kind, userCommand, userImage, fileExtensions, basePath
javo-api /api/sizes                  # sizes you may pick
javo-api /api/workspaces             # the owner's apps
javo-api /api/workspaces/NAME        # .phase (Pending, Starting, Running, Stopped, Failed), .url, .steps[]
javo-api /api/workspaces/NAME/logs   # the app's log
javo-api POST /api/workspaces/NAME/restart
javo-api DELETE /api/workspaces/NAME # the home folder is kept
javo-api /api/jobs                   # the owner's jobs
```

## Deploy an app on Javo

Files are in the owner's home folder, `/home/user`, which is your home folder
here. `file` in a request is a path relative to it.

1. Write the code in its own folder, for a new app `~/agents/<short-name>/`
   (never overwrite a folder that exists). For an existing folder, look at its
   files first.
2. Pick the template from the files: `app.py` that imports streamlit is
   `streamlit` (file: the .py), a Dash app is `dash`, a notebook is
   `jupyterlab` or `voila`, an `index.html` site is `static-site`, a FastAPI
   app is `fastapi`; anything else that serves HTTP runs on `custom` with a
   `command`. `GET /api/templates` lists them with the file extensions each
   opens.
3. Create it:

   ```sh
   javo-api POST /api/workspaces '{"displayName":"Random number","template":"streamlit","file":"agents/random-number/app.py"}'
   javo-api POST /api/workspaces '{"displayName":"My API","template":"custom","file":"agents/my-api","command":"python server.py --port {{.Port}}"}'
   ```

   `name` in the answer is the app's short name.
4. An app must listen on `$PORT` (`{{.Port}}` in a command) on all
   addresses, and serve under the base path it is given (`$BASE_URL`,
   `{{.BaseURL}}`): with `basePath: Prefix` the requests arrive with that
   prefix (pass it to the framework, as streamlit's template does with
   `--server.baseUrlPath`); with `Strip` the prefix is removed and the app
   serves at `/`.
5. Wait for it: poll `javo-api /api/workspaces/NAME` every few seconds until
   `phase` is `Running`, then give the person `url`. The first start installs
   the template's environment and can take a few minutes.
6. When `phase` is `Failed`, or a step stays `False`, read the step that
   failed: its `title`, `what` and `todo` explain it, and its log is
   `javo-api /api/workspaces/NAME/steps/STEP/log` (STEP is the step's `type`,
   such as `EnvReady` or `Started`; `Started` has the app's own output). Fix
   the code and `POST /api/workspaces/NAME/restart`; change a command with
   `PUT /api/workspaces/NAME/launch '{"command":"..."}'`.

To fix a failing app the person names, find it in `GET /api/workspaces`,
read its steps and logs as above, fix the files it runs, and restart it.

## Packages

Each template runs in its own pinned environment (the framework and common
data packages, such as pandas, numpy and altair for streamlit). Prefer what is
there. An app from a Git repository with a `pixi.toml` and `pixi.lock` at its
root runs in that environment instead. Do not install packages into the home
folder at start without asking the person.

## Jobs and schedules

```sh
javo-api POST /api/jobs '{"displayName":"Morning summary","template":"jobs-python","engine":"command","command":"python agents/summary/run.py","schedule":{"cron":"0 7 * * *","timeZone":"Europe/London"}}'
javo-api POST /api/jobs/NAME/run '{}'   # run it now
javo-api /api/jobs/NAME                 # .runs[]: outcome; the log of a run is /api/jobs/NAME/runs/RUN/log
```

A command job runs in the home folder, without a shell. Javo can notify about
failed runs where the admin set that up; it does not send email for a job.
