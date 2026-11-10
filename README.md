# Persia

A Go assistant that keeps repositories distributed: a local Forgejo stays the
source of truth, and Persia creates mirrors on GitHub, GitLab and other
Forgejo instances and sends events to a Matrix room per repository.

## Features

- Discovers managed repositories on a local Forgejo instance through the
  `persia-managed` topic (topics, not labels — labels only exist inside a
  single repository).
- Creates one Matrix room per repository and invites the user.
- Creates repositories on remote forges, configures Forgejo push mirrors over
  SSH (or HTTPS for stubborn targets) and registers deploy keys with write
  access.
- Sends notifications for:
  - new issues / pull requests and their updates (state, title, comment
    count) including a link and a short description, across local and remote
    repositories;
  - push mirror errors and recovery;
  - CI failures (Forgejo Actions, GitHub Actions, GitLab Pipelines).
- Persists room IDs, seen items/pipelines, mirror state and seeding markers
  in SQLite to avoid duplicate notifications.
- CLI commands: `run`, `once`, `doctor`, `version`.
- Built-in HTTP server:
  - `health_addr` — JSON `/healthz`
  - `shortlink_addr` — landing page `https://p.example.org/$RepoName`
    with the mirror list and (when available) the public Matrix room link.
- Per-repository pages are rendered from SQLite at request time through a
  Go `html/template` you supply via `shortlink_template`; a ready-made
  [`shortlink.example.tmpl`](shortlink.example.tmpl) with card layout is
  included. No static-site rebuild is needed.
- Private management room for operational updates (deploy keys, mirror
  recreation, room provisioning).
- Teardown when a repository is no longer managed: push mirrors, deploy
  keys, local SQLite state are removed, the bot leaves the Matrix room with
  a final message.
- Per-user owner filtering, configurable deploy key names, per-target
  transport and push-on-commit toggles.

## How it works

Persia holds no code and does not proxy git. Forgejo performs the
synchronization through push mirrors; Persia only configures mirrors and reads
events from the forge APIs.

```
 local Forgejo         topic: persia-managed
 ssh://git.example.org  │── topic search ──▶ Persia
                        │                    │
                        │                    ├──▶ Matrix: one room per repo
                        │                    ├──▶ remote Forgejo / GitHub / GitLab:
                        │                    │       create repo + deploy key
                        │                    ├──▶ push mirror from Forgejo
                        │                    │
                        │                    ▼
                        │                  polling API ◀── CI, issues, PRs
                        │
 below:  bot polls the apis → Matrix
```

## Quick start locally

Go 1.27+ is required.

```bash
cp config.example.yaml config.yaml
cp .env.example .env
$EDITOR config.yaml   # homeserver, invite_users, owners, targets
$EDITOR .env          # MATRIX_TOKEN, per-target tokens
go build ./...
go run ./cmd/persia doctor -config config.yaml
go run ./cmd/persia once -config config.yaml
go run ./cmd/persia run -config config.yaml
```

Or via make:

```bash
make build
./bin/persia once -config config.yaml
./bin/persia run -config config.yaml
```

## Tagging repositories

Forgejo repositories have **topics** (not labels — labels only exist
inside a single repository). Add the topic `persia-managed` to a
repository's settings.

Additional topics:

| Topic | Meaning |
| --- | --- |
| `persia-managed` | repository is managed by Persia |
| `persia-target-github` | mirror to the `github` target only |
| `persia-target-gitlab` | mirror to the `gitlab` target only |
| `persia-target-altlinux` | mirror to the `altlinux` target only |
| `persia-no-mirror` | do not create mirrors, but still send issue/CI notifications |

If at least one `persia-target-*` topic is set, only those targets are
used. Otherwise `default_targets` from the config (or all enabled targets
when empty) is used.

## Tokens and permissions

### Local Forgejo

Create a token in **Settings → Applications**. Minimum required scopes:

- `write:repository` — topics, push mirrors, deploy keys;
- `read:issue` — issues and pull requests;
- `read:user` — resolve the user.

If Forgejo Actions is enabled, reading workflow runs also needs repository
access (`read:repository`, usually covered by `write:repository`).

### Remote Forgejo (for example altlinux.space)

A token with `write:repository`, `read:issue`, `read:repository`. Persia uses
it to create repositories, add deploy keys and read issues, PRs and CI runs.

### GitHub

A classic PAT with scopes:
- `repo` — create repositories, deploy keys, issues, PRs and Actions data;
- `workflow` — only if the mirrored repository contains `.github/workflows`
  that need updating.

Or a fine-grained token with write access for Administration (create the
repository and deploy keys), Contents (push mirrors and Actions) and read
access for Actions run data. The token must be allowed to create
repositories in the target namespace (personal or organization).

### GitLab

A PAT with the `api` scope. Persia creates projects, adds deploy keys with
`can_push: true` and reads issues, merge requests and pipelines through it.

### Matrix

A dedicated bot account and its access token. Set `matrix.invite_users` to
the Matrix IDs that should be invited to each room. Persia creates private
rooms and invites them.

## Configuration

A complete example lives in
[`config.example.yaml`](config.example.yaml). Main fields:

| Field | Meaning |
| --- | --- |
| `forgejo.base_url` | local Forgejo URL without `/api/v1` |
| `forgejo.token` | token for the local Forgejo |
| `forgejo.username` | used as namespace for local repo creation, fallback owner |
| `forgejo.owners` | whose repositories Persia scans (`"*"` disables the filter) |
| `matrix.homeserver` | bot homeserver |
| `matrix.access_token` | bot access token |
| `matrix.invite_users` | Matrix IDs to invite into each room |
| `topic` | topic that marks managed repositories |
| `target_topic_prefix` | prefix for per-target topics |
| `default_targets` | targets used when no `persia-target-*` topic is present |
| `deploy_key_name` | SSH deploy key title template (`persia {repo}` by default) |
| `sync_interval` | poll interval (default `5m`, minimum `10s`) |
| `mirror_interval` | Forgejo push mirror interval (default `8h`, minimum `10m`) |
| `mirror_sync_on_commit` | push on every commit (default `true`) |
| `mirror_transport` | per target: `ssh` (default) or `https`/`http` |
| `repo_url_template` | per target: browsable URL template for landing pages |
| `template_data` | per target: free-form data passed to templates |
| `description` | per target: template written to remote repo description |
| `database` | SQLite path (`PERSIA_DATABASE` overrides it) |
| `notify_on_first_seen` | announce existing issues/PRs on first scan |
| `notify_cancelled` | treat cancelled CI runs as failures |
| `health_addr` | optional JSON health endpoint (`/healthz`) |
| `shortlink_addr` | optional page server for `/$RepoName` |
| `shortlink_template` | Go `html/template` path, required with `shortlink_addr` |
| `matrix.management_room` | private ops room (`auto` / `disabled` / explicit room ID) |
| `matrix.message_templates` | directory with notification templates (default `./messages`) |
| `matrix.room_alias` | alias template for public rooms |
| `matrix.room_link` | Matrix join link template, reusable with your own frontend |

Secrets come from the environment via `${VAR}`.

### Shortlink template

`shortlink_template` points at a Go `html/template` file. When
`shortlink_addr` is set and the path is empty, unreadable or invalid,
Persia logs the error and panics instead of serving a broken page. The
included [`shortlink.example.tmpl`](shortlink.example.tmpl) is a
self-contained dark-theme page with a repository header, a Matrix
discussion card and a mirror list.

Template data:

| Field | Meaning |
| --- | --- |
| `.Repository` | `owner/name` repository key |
| `.Owner` | repository owner |
| `.Repo` | repository name |
| `.Description` | repository description from the local Forgejo API, empty when unset |
| `.AvatarURL` | repository avatar from the local Forgejo API, empty when the repo has none |
| `.RoomID` | Matrix room ID |
| `.Alias` | public room alias, empty when unavailable |
| `.Public` | whether the room is publicly joinable |
| `.MatrixLink` | join link built from `matrix.room_link`, empty when private |
| `.Mirrors` | list of `.Name`, `.URL`, `.Address` and `.Data` entries |

Each mirror entry carries both a browsable URL and the raw clone address:
`.URL` is a `https://host/namespace/name` link derived from the configured
push mirror address (or rendered from the target's `repo_url_template`),
while `.Address` is the exact SSH or HTTPS URL Forgejo pushes to. Use
`.URL` for human-facing links and `.Address` for copy-paste clone
commands.

`.Data` is the free-form `template_data` map from the target, empty when
unset. Templates can read individual keys with the builtin `index`
function, for example
`{{with index .Data "label"}}<span class="badge">{{.}}</span>{{end}}`.

`repo_url_template` lives on each target and supports the placeholders
`{host}`, `{namespace}`, `{name}`, `{remote}`, `{target}`, `{owner}` and
`{repo}`. The host and namespace are taken from the actual push mirror
address, so per-target namespaces (for example `alice` on GitHub and
`bob` on altlinux.space) are reflected automatically. When the template
is omitted, `https://{host}/{remote}` is used.

`.Description` and `.AvatarURL` are read from the local Forgejo API during
each sync and kept in memory only, so they cost no extra database writes and
never go stale across restarts: the next sync repopulates them. Landing pages
served before the first sync completes simply omit them.

The owner and transport scoping helps when a local Forgejo hosts repositories
from several users or when SSH push mirrors are not usable on a remote
Forgejo: set `mirror_transport: https` and deploy keys stay optional.

## Docker Compose

Docker Compose deployment is still a draft; see [`TODO.md`](TODO.md). Draft
files: [`docker-compose.yml`](docker-compose.yml) and
[`Dockerfile`](Dockerfile):

```bash
cp config.example.yaml config.yaml
cp .env.example .env
docker compose up -d --build
```

For the container the database is forced to `/data/persia.db` in the
`persia-data` volume, so the `database` path in `config.yaml` does not
need changing. The HTTP server respects `health_addr`. To use the
per-repository landing pages behind a reverse proxy, enable
`shortlink_addr` and mount the template into the container. The image runs
from `/etc/persia`, so a relative template path works the same inside and
outside Docker:

```yaml
# docker-compose.yml
volumes:
  - ./shortlink.example.tmpl:/etc/persia/shortlink.example.tmpl:ro
```

```yaml
# config.yaml
shortlink_addr: ":8080"
shortlink_template: "./shortlink.example.tmpl"
```

Both ports are listed under `expose` in `docker-compose.yml`; point the
reverse proxy at the container's `shortlink_addr` port.

The healthcheck in `docker-compose.yml` probes `/healthz` on `:8081`, so
keep `health_addr` in sync with it:

```yaml
health_addr: ":8081"
```

### SQLite backup

The database lives in the `persia-data` volume (path `/data/persia.db`
inside the container). Back it up periodically, for example:

```bash
docker compose stop
docker run --rm -v persia-data:/data -v "$(pwd)":/backup alpine \
  cp /data/persia.db /backup/persia-$(date +%F).db
docker compose start
```

Alternatively use Litestream or `sqlite3 .backup` for 24/7 instances.

### Running without Docker

Use the provided example unit:

```bash
sudo cp deploy/persia.service /etc/systemd/system/persia.service
sudoedit /etc/systemd/system/persia.service  # adjust paths/user
sudo systemctl enable --now persia
```

## Why push mirrors use SSH

Forgejo supports push mirrors over HTTPS (username/token) and SSH. During SSH
authorisation Forgejo generates a per-mirror Ed25519 pair and exposes its
public key through the API. Persia registers that key on the target forge:

- GitHub: `POST /repos/{owner}/{repo}/keys` with `read_only: false`;
- GitLab: `POST /projects/{id}/deploy_keys` with `can_push: true`;
- Forgejo: `POST /repos/{owner}/{repo}/keys` with `read_only: false`.

Each repository and target gets its own key, so no shared highly privileged
account is needed.

The registered key title is configurable through `deploy_key_name`
(globally) or `targets.<name>.deploy_key_name`. Placeholders:
`{repo}`, `{owner}`, `{name}`, `{target}`, `{namespace}`, `{remote}`.

### Push on commit

By default Forgejo pushes on every commit (`sync_on_commit`). Disable
globally with `mirror_sync_on_commit: false`, or per target with
`targets.<name>.mirror_sync_on_commit`. Forgejo has no API for editing
an existing push mirror, so Persia recreates it when the setting changes,
rotating the SSH key and registering a new deploy key. The old key on the
target repository is left in place.

### Known SSH requirements

- Forgejo must be able to run `ssh` and trust the target host key. Add
  the host to the `known_hosts` file of the account running Forgejo if
  SSH errors mention host key verification.
- GitHub and GitLab use the `git` user.
- If SSH runs on a non-standard port, set `ssh_port` on the target.
- Some remote Forgejo instances (for example altlinux.space) refuse
  push authorisation through repo-level deploy keys even when they are
  registered as write-enabled. In that case switch the affected target to
  `mirror_transport: https`.

## Notifications

One Matrix room per local repository gathers events from all provisioned
mirrors, tagged with the source (`github`, `gitlab`, `altlinux` or the
local Forgejo hostname):

- new and updated issues / pull requests;
- status, title or comment count changes on existing items, each with a
  link;
- CI failures (Forgejo Actions, GitHub Actions, GitLab Pipelines);
- push mirror failures and recovery.

Separate private management room stores operational messages: room creation,
deploy key additions, recreation of mirrors and cases where a mirror key is
already registered as a regular user SSH key.

On first discovery the database is seeded quietly; set
`notify_on_first_seen: true` if the current state should also be announced.

### Message templates

Every word Persia sends to Matrix comes from a template file, so the wording
can be changed without rebuilding the binary. Point `matrix.message_templates`
at a directory; the shipped [`messages/`](messages) directory is the default.

Each `<event>.tmpl` file must define two templates:

```
{{define "text"}}plain-text fallback for clients without HTML{{end}}

{{define "html"}}org.matrix.custom.html body{{end}}
```

Available events: `new_issue`, `updated_issue`, `pipeline_failed`,
`pipeline_recovered`,
`mirror_failed`, `mirror_recovered`, `room_created`, `room_retired`,
`room_initialized`, `room_provisioned`, `mirror_recreated`,
`deploy_key_added`, `deploy_key_exists_as_user`,
`deploy_key_removed_stale`, `deploy_key_removed_unmanage` and
`repo_retired`.

Templates receive a single struct; only the fields relevant to an event are
filled in:

| Field | Meaning |
| --- | --- |
| `.Repo` | `owner/name` repository key |
| `.Source` | event source (`local`, `github`, `gitlab`, `altlinux`) |
| `.Target` | mirror target name |
| `.URL` | issue, pull request or pipeline URL |
| `.Kind` | `issue` or `pull_request` |
| `.Number` | issue or pull request number |
| `.Title` | issue or pull request title |
| `.Body` | issue or pull request description |
| `.Changes` | list of change descriptions for updates |
| `.Name` | pipeline name |
| `.Ref` | pipeline ref |
| `.SHA` | pipeline commit hash |
| `.Status` | pipeline status |
| `.Error` | push mirror error text |
| `.RepoURL` | repository web URL |
| `.RoomID` | Matrix room ID |
| `.KeyTitle` | deploy key title |
| `.SyncOnCommit` | whether the mirror pushes on every commit |
| `.TargetData` | free-form `template_data` map of the target, empty when unset |

The free-form `template_data` configured on a target is passed through to
templates as `.TargetData`. Templates can read a key with the builtin
`index` function, for example
`{{with index .TargetData "label"}}[{{.}}]{{end}}`.

Helpers: `{{kind .Kind}}` (human label), `{{excerpt 300 .Body}}` (trimmed
description with an ellipsis), `{{join ", " .Changes}}`,
`{{short .SHA 8}}`, `{{sourceLabel .}}` (target `label` from
`template_data`, otherwise the source name) and `{{targetData . "key"}}`
(an arbitrary key from `template_data`).

`html` templates are rendered with `html/template`, so values are escaped
automatically. A missing directory, a missing event, a template without
`text` or `html`, or a syntax error aborts startup with an error naming the
offending file.

## Development

```bash
make fmt
make vet
make test
make build
```

Structure:

```
cmd/persia/          CLI
internal/config/     YAML config and validation
internal/forge/      Forgejo, GitHub, GitLab adapters
internal/matrix/     minimal Matrix client-server API
internal/message/    Matrix notification templates
internal/store/      SQLite state
internal/engine/     reconcile loop and notifications
messages/            notification templates (text and html per event)
```

Test without sending notifications:

```bash
go test ./...
go run ./cmd/persia doctor -config config.yaml
```

## Limitations

- Updates are driven by polling the forge APIs; webhooks are not used, so
  latency is bounded by `sync_interval` and API rate limits.
- Persia does not delete remote repositories when a topic is removed.
- Push mirrors and Matrix rooms are cleaned up on unmanage; the per-repo
  room is left with a final message because the bot cannot delete it
  through the Synapse client API.
- Comment notifications only track the counter, not the comment text.
- Dropping the SQLite database creates fresh rooms on the next run.
- On first discovery existing failures are seeded silently.

Future work lives in [TODO.md](TODO.md).