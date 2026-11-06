# Using the Javo API from this app

Javo runs apps (web apps, notebooks, any published container image) and jobs
(one-off or scheduled) for the person who owns this app. You act as that
person through the Javo API.

- `$JAVO_URL` is the Javo web address, for example https://javo.example.com.
- `$JAVO_TOKEN` is the token. Never print it, write it to a file or commit it.

Call the API with `javo-api [METHOD] PATH [JSON]` (GET by default). It sends
the token, prints the answer as JSON, and exits non-zero on an error answer.
The full API, with every field, is the OpenAPI document at
`$JAVO_URL/api/openapi.json`. Errors are JSON problems: `title` says what went
wrong and `errors` names the field.

## What this token may and may not do

- Read everything the owner can see, except secrets.
- Create, change, start, restart and delete the owner's apps and jobs, and
  run, pause and resume jobs, as long as the app or job holds no secret, API
  token or Nebi token.
- Not allowed (answers 403): reading or storing secrets, giving an app or job
  a secret or API access, sharing an app, anything on an app or job that
  already holds a secret (even a restart), and creating coding-agent apps.
  Ask the person to do it in the browser. For a start, stop, restart, delete
  or resize you may not do, propose it for them to approve:
  `javo-api POST /api/plans '{"source":"mcp:agent","proposal":{"action":"restart","target":"NAME"}}'`.
- Files: the owner's home folder is your home folder (`/home/user`), so write
  files there directly. Every app and job sees the same home.
- SSH: `git clone git@github.com:...` and `ssh` work through an ssh-agent
  (`$SSH_AUTH_SOCK`) that holds the owner's keys. The private keys are not
  readable here: `~/.ssh` is a folder of this app with only `known_hosts`,
  `config` and the public keys, and what you write to it is lost at restart.
  Keys with a passphrase are not loaded; `ssh-add -L` lists the ones that are.

## Look around first

```sh
javo-api /api/me                     # who you act as
javo-api /api/templates              # .items[]: name, kind, userImage, userCommand, port, basePath, fileExtensions
javo-api /api/sizes                  # .items[]: name, cpu, memory (bytes), default, fits
javo-api /api/idle-stop              # allowed idle-stop minutes
javo-api /api/environments           # pixi environments an app or job may run in
javo-api /api/workspaces             # the owner's apps
javo-api /api/jobs                   # the owner's jobs
javo-api /api/secrets                # 403 for you: secrets are the person's
```

## Run code from the home folder

1. Write the code in its own folder, for a new app `~/agents/<short-name>/`
   (never overwrite a folder that exists). For an existing folder, read its
   files first.
2. Pick the template from the files: a Streamlit `app.py` is `streamlit`
   (file: the .py), a Dash app is `dash`, a notebook is `jupyterlab` or
   `voila`, an `index.html` site is `static-site`, a FastAPI app is `fastapi`;
   anything else that serves HTTP runs on `custom` with a `command`.
   `GET /api/templates` lists them with the file extensions each opens.
3. Create it:

   ```sh
   javo-api POST /api/workspaces '{"displayName":"<Title>","template":"streamlit","file":"agents/<short-name>/app.py"}'
   javo-api POST /api/workspaces '{"displayName":"<Title>","template":"custom","file":"agents/<short-name>","command":"pixi run python server.py --port {{.Port}}"}'
   ```

   `name` in the answer is the app's short name (NAME below). Optional
   fields: `size` (a name from `/api/sizes`), `idleStopMinutes`, `env`
   (plain values: `[{"name":"LOG_LEVEL","value":"info"}]`), `environment`
   (`{"id":"<id from /api/environments>"}`).
4. The app must listen on `$PORT` (`{{.Port}}` in a command) on all
   addresses, and serve under its base path (`$BASE_URL`, `{{.BaseURL}}`):
   with `basePath: Prefix` requests arrive with that prefix (pass it to the
   framework); with `Strip` the prefix is removed, the app serves at `/`, and
   the prefix is in the `X-Forwarded-Prefix` header. With `Strip` most apps
   work as they are: the gateway sends the page's requests for absolute
   paths (`/assets/...`, `/api/...`) to the app and adds the prefix to its
   redirects. A base path setting is still better when the app has one.

Each template runs in its own pinned environment with its framework and
common data packages; prefer what is there. A Git repository with a
`pixi.toml` and `pixi.lock` at its root runs in that environment instead. Do
not install packages into the home folder at start without asking.

## Python: always through pixi

Every Python project you start gets its own pixi workspace in its folder,
and every Python command runs through it. Never call `python3`, `pip` or
`python -m venv` directly, and never `pip install` into the home folder.

```sh
cd ~/agents/<short-name>
pixi init                          # pixi.toml for this project
pixi add python=3.12 <packages>    # from conda-forge; pixi add --pypi <pkg> only for what conda-forge lacks
pixi run python app.py             # run, test and debug through pixi run
```

Keep `pixi.toml` and `pixi.lock` in the folder. When the project becomes an
app on the `custom` template, its command runs through pixi too
(`pixi run python server.py --port {{.Port}}`); for a framework template
(streamlit, dash, fastapi...), add to the project only what that template's
environment lacks.

## Deploy a published container image

When the person asks to deploy a well-known web app that ships an image, run
that image on the `container` template. If `GET /api/templates` has no
template with `userImage: true`, the admin turned images off: tell the person.

1. Find the image the project itself publishes (its docs, its own
   organisation on the registry, or an official or verified image). Avoid
   third-party repackagings.
2. Pin it: an exact version tag, and the digest too when you can read it
   (`<image>:<version>@sha256:<digest>`). Never `latest` or a moving tag such
   as a bare major version.
3. From the image's docs, note:
   - The port it listens on. The app runs as the owner's user, never root,
     with no extra capabilities, so it cannot bind ports below 1024 and
     cannot run steps that need root (installing packages, changing owners).
     Pick a variant that runs as any user (often called unprivileged or
     rootless), or set the image's own port setting to a port above 1024.
   - Where it writes data. Only `/home/user` (the home, kept), `/tmp` and
     `/scratch` (lost on restart) and the image's own layer (lost on restart)
     are writable. Point its data directory at
     `/home/user/agents/<short-name>/data` (create the folder first) so data
     survives a restart.
   - Whether it can run under a URL path. The gateway strips the app's
     prefix (`basePath: Strip`): requests arrive at `/`, with the prefix in
     `X-Forwarded-Prefix`. If the app's `url` has a path after the host and
     the app writes absolute links, it usually works as it is (the gateway
     sends those requests to the app), but when the app has a public URL or
     root path setting, set it to that `url`: only a setting that changes the
     links it writes, not one that makes it expect the prefix on incoming
     requests. On its own subdomain (`url` ends at the host) nothing is
     needed.
   - The settings it needs, as environment variables or command arguments.
4. Pick a size from `GET /api/sizes` that holds the memory the docs ask for
   (`fits: true`); leave `size` out for the default.
5. Create it:

   ```sh
   javo-api POST /api/workspaces '{"displayName":"<Title>","template":"container","image":"<image>:<version>@sha256:<digest>","port":<port>,"size":"<size>","env":[{"name":"<SETTING>","value":"<value>"}]}'
   ```

   `command` is optional and replaces the image's default command (its
   CMD; its ENTRYPOINT still runs). It is split into words like a shell
   line but runs without a shell, and may use `{{.Port}}`, `{{.BaseURL}}`
   and `{{.Home}}`. `PORT` is set to the port. A refused registry answers
   422 on `image` and names the registries allowed.
6. Credentials (passwords, API keys, database URLs) never go in `env`
   values, a command or a file. Create the app without them, then ask the
   person to store each one in Settings > Secrets and add it to the app's
   environment in the browser. After that the app holds a secret and is
   theirs to change, so finish everything else first.

To change a running image app: `PUT /api/workspaces/NAME/launch` with
`{"image":"...","port":...,"command":"..."}`, `PUT /api/workspaces/NAME/env`
with `{"env":[...]}` (the list replaces the one before), and
`PUT /api/workspaces/NAME/size` with `{"size":"..."}`.

## Wait, read the logs, fix and retry

```sh
javo-api /api/workspaces/NAME                  # .phase: Pending, Starting, Running, Stopped, Failed; .url; .steps[]
javo-api "/api/workspaces/NAME/logs?tail=200"  # .lines[]; add &previous=true after a crash
javo-api /api/workspaces/NAME/diagnosis        # the cause Javo found and what may fix it
javo-api /api/workspaces/NAME/events           # Kubernetes events (image pulls, scheduling)
javo-api POST /api/workspaces/NAME/restart
javo-api DELETE /api/workspaces/NAME           # the home folder is kept
```

Poll the app every few seconds until `phase` is `Running`, then give the
person its `url`. A first start can take a few minutes (installing an
environment, pulling an image). When `phase` is `Failed` or a step stays
`False`, read that step's `title`, `what` and `todo`, and its log at
`/api/workspaces/NAME/steps/STEP/log` (STEP is the step's `type`):

- `ImageReady` failed: the image or tag does not exist or the registry is
  private. Check the reference.
- `Started` failed or keeps restarting: read the logs (`previous=true`). A
  permission error means it wants root or a folder it cannot write.
- `Ready` stays `False`: it is not listening on the port you gave. Check the
  port and that it listens on all addresses.
- `Started` with reason `OutOfMemory`: pick a bigger size.

Fix it with the calls above, restart, and check again. To fix an app the
person names, find it in `GET /api/workspaces` and do the same.

## Stop when idle, and share

`PUT /api/workspaces/NAME/idle-stop` with `{"idleStopMinutes":60}` stops an
unused app after that long, within `GET /api/idle-stop` (0 is off where
`canTurnOff` is true).

Sharing is the person's to do in the app's sharing settings in the browser:
Private (only them), Restricted (named people and groups), SignedIn (everyone
who can sign in) or Public (anyone with the link), up to the template's
`maxSharing`. `GET /api/workspaces/NAME/sharing` shows the current setting.

## Jobs and schedules

```sh
javo-api /api/engines                   # what each engine runs (papermill, command, bash, ...)
javo-api POST /api/jobs '{"displayName":"<Title>","template":"jobs-python","engine":"command","command":"python agents/<short-name>/run.py","schedule":{"cron":"0 7 * * *","timeZone":"Europe/London"}}'
javo-api POST /api/jobs/NAME/run '{}'   # run it now
javo-api /api/jobs/NAME                 # .runs[]; a run's log is /api/jobs/NAME/runs/RUN/log
javo-api POST /api/jobs/NAME/pause      # pause a schedule; /resume starts it again
```

Leave `schedule` out for a job that only runs on demand. A command job runs
in the home folder without a shell; a notebook job names its `source` (a path
in the home) with an engine such as `papermill`. Change a job with
`PUT /api/jobs/NAME` (the whole job), its size with `PUT /api/jobs/NAME/size`
and its plain environment with `PUT /api/jobs/NAME/env`. Javo can notify
about failed runs where the admin set that up; it does not send email.

## MCP

Agents outside Javo reach the same API at `$JAVO_URL/mcp` with a personal
token: reads, and changes only as plans the person approves. Inside this app,
use `javo-api`.
