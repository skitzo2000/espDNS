# Controller HTTP API

The controller serves its dashboard and a JSON API on `http://127.0.0.1:8480` (set by
`ESPDNS_LISTEN`, loopback only unless it runs behind an HTTPS reverse proxy, below). The
dashboard uses this API, and so can you, from the same machine.

Every route in 0.0.x (alpha) is here. The API may change in any 0.0.x release; there is no
versioned path.

Back to the [reference index](README.md).

## Before you call it

### Where requests may come from

- The controller listens on loopback only and refuses to start on any other address,
  unless `ESPDNS_URL` and `ESPDNS_PROXY` are set ([security.md, Behind a reverse
  proxy](../security.md#behind-a-reverse-proxy)).
- Every request under `/api/` and `/build/` must have a `Host` of `localhost` or a loopback
  address, with any port, or the host of `ESPDNS_URL`. Others get `403`. This stops DNS
  rebinding.
- A request that changes something must not come from another site: an `Origin` header, if
  present, must match the `Host` (by `ESPDNS_URL`'s host: exactly its origin), and
  `Sec-Fetch-Site: cross-site` is refused (`403`).
- With `ESPDNS_URL` set, a connection from anywhere but this machine or an `ESPDNS_PROXY`
  address gets `403` for every path, and a `Host` other than the URL's (or, from this
  machine, a localhost name) too.
- The controller never answers CORS. A page on another site can't call it.

### Logging in

A session has two halves. Send both on every request under `/api/`, and on every non-GET,
non-HEAD request:

1. A cookie named `espdns_session_<port>` (for example `espdns_session_8480`). It is
   `HttpOnly` and `SameSite=Strict`, and `Secure` when you came by `ESPDNS_URL`.
   `POST /api/login` sets it.
2. A header `X-Session-Token: <token>`, with the `token` from the login reply.

To log in:

```http
POST /api/login HTTP/1.1
Host: 127.0.0.1:8480
Content-Type: application/json

{"user": "admin", "password": "your-password"}
```

The reply sets the cookie and returns:

```json
{"user": "admin", "token": "…", "device": "…", "expires": "…"}
```

A session ends after 2 hours idle, or 12 hours after login. Failed logins back off per client
(`429` with `Retry-After`). Send the `device` value back in `X-Login-Device` on later logins
to be counted as the same client.

### Access levels

| Level | Meaning |
|-------|---------|
| public | No session needed. |
| open | A GET that anyone on this machine may call while no password is set. Once a password is set, it needs a session. |
| login | Always needs a session. While no password is set, it answers `403` ("read-only"). |
| login + password | Needs a session and a fresh password grant (see below). |

Any request other than GET or HEAD needs a session; while no password is set it gets `403`
("read-only"). Set the first password with
[`POST /api/setup/password`](#login-and-session) or [`espdns passwd`](cli.md#passwd).

Once a password is set, an API request without a session gets `401` and a page request is
redirected to `/login.html`.

### Password grants

Three things need your password again: changing it, downloading a backup, and showing a
config's Wi-Fi password. Call `POST /api/reauth` with `{"password": "…"}` to get a grant:

```json
{"reauth": "…", "expires": "…"}
```

Send it in `X-Reauth: <grant>` with the request that needs it. A grant lasts 2 minutes and
covers one request. Without one, the request gets `403` with `{"reauth": true}`.

### Bodies and replies

- Request bodies are JSON with `Content-Type: application/json`. Routes that need a login,
  the login routes and the job routes refuse a POST without it (`403`). Install over USB's
  four POSTs (`/api/boards/check`, `/api/boards`, `/api/build`, `/api/build/keys`) don't
  check it.
- Most bodies refuse unknown fields (`400`).
- Replies are JSON with `Cache-Control: no-store`, unless noted.
- An error reply is `{"error": "…"}`. Some also name the form field: `{"error": "…", "field": "…"}`.
- Files you edit (configs, zones, blocking sources, list definitions, settings) carry a
  version (`hash` or `version`). Send back the one you read; if the file changed since, the
  save gets `409` and changes nothing. Use `""` for a new file.

## Routes

### Login and session

| Method | Path | Level | Body | Reply |
|--------|------|-------|------|-------|
| GET | `/api/session` | public | — | `password_set`, `logged_in`, `user`, `expires`, `version` (the controller's), `error`. |
| POST | `/api/login` | public | `{"user", "password"}` | Sets the cookie. `{"user", "token", "device", "expires"}`. `401` wrong login, `429` backing off. |
| POST | `/api/logout` | public* | — | Ends the session, clears the cookie. `{"logged_in": false}`. |
| POST | `/api/reauth` | login | `{"password"}` | `{"reauth": "<grant>", "expires"}`. `403` wrong password. Too many wrong passwords end the session (`401`). |
| POST | `/api/password` | login + password | `{"password"}` | Changes the logged-in user's password. `400` with `"field": "password"` if the new one isn't taken (checked before the grant is used). Every session ends, this one too: log in again. |
| POST | `/api/setup/password` | public | `{"user", "password"}` (`user` `""`: `admin`) | Sets the first password, then logs in as `/api/login` does. Only while no password is set (`409` after). Only from a loopback client address (`403` otherwise). Password: at least 12 characters. |

\* `/api/logout` is a POST, so in practice it needs the session it ends.

### Nodes

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| GET | `/api/nodes` | open | — | Every node the controller knows: `id` (chip MAC), `addr`, `hostname`, `mac`, `source` (`settings`, `mdns` or `lookup`), `out_of_service` (one in `settings.json` taken out of service), `online`, `last_seen`, `polled`, `error`, `qps`, and `status` (the node's last [`/status`](node-endpoints.md#status), kept while it is offline). |
| GET | `/api/nodes/found` | login | — | `{"nodes": [...]}`: nodes found over mDNS or looked up that are not in `settings.json`, each with what adds it in `next`: `adopt`, `settings-add`, or `""` (its `refusals` say why). |
| POST | `/api/nodes/lookup` | login | `{"address": "192.0.2.10"}` | Reads `/status` at that IPv4 address only. `{"address", "listed", "found", "answered", "error", "node"}`. A node found is listed from then on, as an mDNS one is. |
| GET | `/api/nodes/{id}/settings` | login | `{id}`: the node ID | The node page's form: each field's value, where it comes from, and whether a change applies live or at a restart; the config file's `version`; its pending changes. Never the Wi-Fi password. |
| POST | `/api/nodes/{id}/settings` | login | `{"settings": {...}, "version"}` | Checks the edit and adds it as a pending change of the node's config file. Returns the form again. `settings` takes `name`, `network`, `forwarders`, `tz`, `blocking` (bool), `querylog` (`{"enabled", "client"}`), `led` (the status light, as a config's [`led`](node-config.md#led)), and `reset` (fields to put back to the lower layer). Only for a node in `settings.json`. An edit that moves the node to another address is refused with `"move"`: the new address. A node whose config file is missing or doesn't read is answered (both routes) with `"write"`: `[{"file", "draft"}]`, the files to write or correct. |
| POST | `/api/nodes/{id}/move` | login | `{"settings": {"network"}, "version"}` | Writes only the new address to the node's config file, at once, for the `config-push` job to push next (a dry run, then the push, which confirms the node there and lists it there in `settings.json`). Refused while changes of the file wait to apply. Returns `{"file", "node", "to", "hash"}`. |
| POST | `/api/nodes/{id}/move/undo` | login | `{"hash"}` | Puts the config file back as it was before a move whose push didn't go through, while it is still as the move wrote it. |

### Fleet state

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| GET | `/api/key` | open | — | The release key: `source`, `present`, and `fingerprint`, or `error` and `missing`. Never the key. `recovery`: the recovery public key's `present`, `fingerprint`, or `error` and `missing`. `nodes_listed`: `settings.json` lists a node. `rotation`: a key rotation under way, `{"id", "release", "recovery"}` (its new keys' fingerprints), or `null`. |
| POST | `/api/keys/release` | login | `{"reinstall": false}` | Makes a release key in `keys/release.pem`, as `espdns key generate`. `409` if one is there, or a node is listed and `reinstall` isn't set (the listed nodes trust another key and refuse this one until installed again over USB). `/api/key`'s reply. |
| GET | `/api/keys/release.pub` | open | — | The release key's public half, the 65-byte point, as `release.pub`. `404` without a key. |
| POST | `/api/keys/recovery` | login | `{"public_key": "<base64 of the 65-byte point>", "replace": false}` | Sets the recovery key's public half, `keys/recovery.pub` (made in the browser, or read from a file there; any other field, a private key, is refused). The same key again changes nothing. Another: `409` unless `replace`, and always while a node is listed; `409` too if a listed node that trusts the controller's release key trusts another recovery key. `400` with `field` for a malformed key or the release key. `/api/key`'s reply. |
| GET | `/api/keys/rotation` | open | — | `{"pending", "rotations"}`: the rotation under way (or `null`) and the last ten, newest first. Each is its `plan.json` (`id`, `old` and `new` keys' fingerprints, `compromised`, `nodes` with each one's `state`: `prepared`, `keys` or `done`, `repush`, `missing`, `error`; `finished`), with `signed` (by node ID), `signed_all` and `sign_command` (the offline signing, from the controller's directory). |
| POST | `/api/keys/rotation` | login | `{"new_release": "generate"\|"keep", "new_recovery": "<base64 of a public key>", "recovery_pub": "<base64>", "compromised": false}` | Prepares a rotation for every node in `settings.json`, as `espdns key rotate prepare` (the nodes' `/status` read now). `new_recovery` empty: the recovery key stays; `recovery_pub`: the one the nodes trust, if `keys/recovery.pub` isn't there. Any other field (a private key) is refused. `201` with the rotation; `409` with why (a rotation under way, a node out of service, not pinned or not answering, ...). |
| POST | `/api/keys/rotation/{id}/signed` | login | `{"release": "<base64 of a node's release.bin>"}` | A node's KEYS release signed elsewhere: checked (the release prepared for one of the rotation's nodes, signed with the recovery key they trust) and kept. The rotation. `400` with `field` if it isn't. |
| POST | `/api/keys/rotation/{id}/discard` | login | `{}` | Removes a rotation no node has taken (and `keys/release.next.pem`). `409` once a node took it. |
| GET | `/api/lock` | open | — | `{"held": false}`, or `{"held": true, "holder", "text"}`: who holds the fleet lock. |
| GET | `/api/actions` | open | `?n=` (default 100) | The action log's newest `n` entries, newest first. |
| GET | `/api/dashboard` | open | — | The dashboard: `now`, `window_s`, `bin_s`, `bins`, `fleet` (rates and time series: queries a second, blocked, p95 latency, upstream p95 and failures, cache hit rate, SERVFAIL) and `nodes`. Built from each node's `/metrics`, in memory only. |
| GET | `/api/querylog` | login | `?node=&client=&name=&result=&qtype=&after=&run=&limit=` | The query log the controller holds in memory, newest first: `entries`, `run`, `last`, `matched`, `held`, `cap`, `oldest`, `newest`, `nodes`. `limit` default 200, at most 1000. To follow, send the last reply's `last` as `after` and its `run` as `run`. |
| GET | `/api/querylog/top` | login | `?node=` | The names asked most, in what the controller holds. |
| GET | `/api/search` | login | `?q=` | Nodes, zones, records, devices, sites, rules, lists and pending changes that match. Reads only what the controller holds. Takes at most 2 seconds. |

### Setup and settings

| Method | Path | Level | Body | Reply |
|--------|------|-------|------|-------|
| GET | `/api/setup` | open | — | What is set up: the password, `settings.json`, the keys (`release_key`, `recovery_key`, `keys_saved`), the zone primary (`primary`: its address), the nodes, and `zones`, the zones of your own as the Zones page counts them (hosted zone files and hosted zones waiting as pending changes, and the secondary and forward zones the node configs hold as the pending changes make them; the firmware's default zones, in no config, don't count). `first_run` while there is neither a password nor `settings.json`. The first run's steps (`password`, `keys`, `first_node`, `second_node`, `zones`, `router`, `blocking`), and `done` once every one is; `blocklists`, the lists defined. `keys` is done with both keys there and `keys` marked; `zones` with a zone of your own or `zones` marked. |
| POST | `/api/setup/step` | login | `{"step"}`: `keys`, `zones`, `router` or `blocking` | Marks a first-run step done that the controller can't see done (recovery.pem saved offline: only once both keys are there; the router told) or that was skipped (zones, blocking). Kept in `setup.json` in the data directory. `400` with `field` for another step (`primary`, a step in 0.0.2 and before, among them; an older `setup.json` with it marked is read fine and the mark ignored). The setup again. |
| GET | `/api/settings` | open | — | `settings.json` as the form edits it (every field, empty ones too), its `version`, and `primary`: the zone primary's `address`, `allows` and `zones`, `set`, and `uncovered`, the nodes in `settings.json` whose address is in none of the networks it allows. |
| POST | `/api/settings` | login | `{"settings": {...}, "version"}` | Replaces `settings.json` whole if it is still `version` (`""` while there is no file; `409` otherwise). Returns the form again. A refused field: `{"error", "field"}`. See [settings.md](settings.md#settingsjson). |

### Node configs

Node configs are files in `<data>/configs`. See [node-config.md](node-config.md).

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| GET | `/api/configs` | login | — | `{"dir", "configs", "nodes"}`: every config and the nodes that run each. |
| GET | `/api/configs/{name}` | login | `?reveal=1` (needs a [password grant](#password-grants)) | The config's text and history. The Wi-Fi password is hidden unless revealed. |
| POST | `/api/configs/{name}/check` | login | `{"text", "board", "node"}` | The checks, as the node runs them. Never saved. |
| POST | `/api/configs/{name}` | login | `{"text", "hash"}` | Saves the file (`hash`: the version edited; `""` for a new file). `409` if it changed. Where the text still has the hidden password marker, the saved password is kept. |
| POST | `/api/configs/{name}/delete` | login | `{"hash"}` | Deletes the file, kept in its history. `409` if it changed, if a node runs it, or if an adoption or a push gave it to a node. |

### Hosted zones

Hosted zones are RFC 1035 master files in `<data>/zones/<zone>.zone`.

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| GET | `/api/zones` | login | — | Every zone file, the nodes serving each, the nodes, the deleted ones. |
| GET | `/api/zones/{name}` | login | `?version=<file>` | The zone's text, hash and history; with `version`, a kept version's text. |
| GET | `/api/zones/{name}/new` | login | — | A new zone's text from the template: SOA, and NS for the nodes in `settings.json`. |
| POST | `/api/zones/{name}/check` | login | `{"text"}` | The zone checks, then the checks against each node. |
| POST | `/api/zones/{name}/serial` | login | `{"text"}` | `{"text", "serial", "was"}`: the text with the SOA serial raised above the saved file's and the nodes'. |
| POST | `/api/zones/{name}` | login | `{"text", "hash"}` | Saves the file. `409` if it changed. |
| POST | `/api/zones/{name}/delete` | login | `{"hash"}` | Deletes it. The file is kept in the history. |
| GET | `/api/zone-inventory` | login | — | Every zone the fleet answers for (hosted, secondary, forward), its source and the fleet's state, and the zone primary (`address`, `allows`, `zones`, `set`; no node named). |
| GET | `/api/zone-records` | login | `?name=<zone>` | A copied zone's records (`name`, `serial`, `records`, `more`): from the node with the newest copy (`node`, its name), else by zone transfer from its `primary` (`node` empty). `502` with `kind` (`refused`, `unknown`, `timeout`, `unreachable`, `failed`) when that transfer fails. |
| GET | `/api/zone-lookup` | login | `?name=<zone>` | Where a zone lives: the fleet's already, a configured primary's, or none. Sends each configured primary one SOA query. |

### Blocking

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| GET | `/api/blocking` | open (part) | — | The list definitions, source files, compiled files, the nodes' blocking, each list's last compile job and its CLI command. Without a session (no password set), only the nodes' blocking and the compiled files. |
| GET | `/api/blocking/defs` | login | `?version=<file>` | A kept version of the list definitions. |
| POST | `/api/blocking/defs` | login | `{"lists": [...], "hash"}` | Saves the list definitions (`blocking/lists.json`). `409` if changed. |
| GET | `/api/blocking/sources/{name}` | login | `?version=` | One source file: its text (the start of a big one), hash and history. |
| POST | `/api/blocking/sources/{name}` | login | `{"text", "hash"}` | Saves it (`""` hash: a new file). At most 32 MiB. |
| POST | `/api/blocking/sources/{name}/delete` | login | `{"hash"}` | Deletes it, kept in the history. Refused for a file a list reads. |
| POST | `/api/blocking/refresh` | login | `{}`, or `{"list": "<name>", "accept_change": true}` | `{}`: checks every downloaded list for a new version now (`202`, `{"checking": true}`; `409` while a check runs). With `accept_change`: sends that list's new version, which the size-change check refused, this once (a [`refresh`](#job-kinds) job, `202`, `{"job"}`). |
| POST | `/api/blocking/refresh/schedule` | login | `{"every": "off" \| "6h" \| "12h" \| "24h" \| "168h"}` | Saves how often downloaded lists are checked (`settings.json`'s `blocklist_refresh`; `"24h"`, the default, is saved as none). `400` with `field` for another value. `{"every"}`. |

A compile is a job: kind [`blocklist`](#job-kinds). Nothing here pushes a list; a
[refresh](#the-refresh) does. `GET /api/blocking/setup` (the Blocking page) has `refresh`:
`every`, `choices`, `checked`, `updated`, `next`, `outcome` (`unchanged`, `updating`,
`updated`, `refused`, `failed` or `waiting`), `message`, `over` (a refusal's numbers),
`checking` and `sending`; and each of its `lists` a `download`, `updated` and `checked`.

### Pending changes and updates

Edits wait here as pending changes until one [`apply`](#job-kinds) job sends them all.

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| GET | `/api/changes` | login | — | `{"changes", "restarts", "apply"}`: the pending changes, oldest first; the nodes that restart; the apply running or the last one. |
| POST | `/api/changes` | login | A file change: `{"kind", "name", "text" or "delete": true, "hash", "summary"}`. A firmware update: `{"kind": "firmware", "firmware", "nodes", "summary"}` | `201` with the change. `kind`: `config`, `zone`, `source`, `lists` or `firmware`. A firmware update goes only to nodes in `settings.json`. |
| GET | `/api/changes/file` | login | `?kind=&name=` | A file as the pending changes make it: what an edit starts from. |
| POST | `/api/changes/{id}/discard` | login | — | Discards it, and the changes of its file stacked on it. |
| POST | `/api/changes/discard` | login | — | Discards every pending change but the put-back ones. |
| GET | `/api/updates` | open | — | Each node's firmware against the newest version (`newest`), and the builds. |
| POST | `/api/updates` | login | `{"nodes": [...]}` | `201` `{"changes": [...]}`: the updates, added as pending firmware changes, one per chip image. No `nodes`: every node offered one. Nothing is pushed or downloaded until an apply. |

### Rolling push

| Method | Path | Level | Body | Reply |
|--------|------|-------|------|-------|
| GET | `/api/push/sources` | login | — | What there is to push, the nodes, the defaults. |
| POST | `/api/push/preview` | login | A [rollout request](#rollout-request) | What each node would get, from its `/status`. Read only, no job. |

The push itself is a job: kind [`rollout`](#job-kinds).

### Adoption

| Method | Path | Level | Body | Reply |
|--------|------|-------|------|-------|
| GET | `/api/adopt` | login | — | The nodes found (unadopted ones to adopt; adopted ones not in `settings.json`), the configs, the zone primary (as `GET /api/settings` has it). |
| POST | `/api/adopt/preview` | login | An [adoption request](#adoption-request) | The address it would get, the config checked for the node, the zones, and `advice`: what to allow once on the zone primary, when the node's address is in none of the networks it allows. Read only, no job. |

Adoption itself is a job: kinds [`adopt` and `settings-add`](#job-kinds). Nothing changes on
the zone primary. The job's progress has `primary`: `{"primary", "zones", "advice"}`, with
`advice` set when the node's address is in none of the networks the primary allows:
"Your zone primary must allow zone transfers from 192.0.2.60: allow its network (e.g.
192.0.2.0/24) there once." It's said once and never stops the adoption.

### Boards and flashing

| Method | Path | Level | Body | Reply |
|--------|------|-------|------|-------|
| GET | `/api/boards` | open | — | The board catalog and your own boards, each with `image_ready` (its chip image is imported). |
| POST | `/api/boards/check` | session | A board definition | `{"errors": [...], "warnings": [...]}`. |
| POST | `/api/boards` | session | A board definition | Saves it to `<data>/boards`. `201` with the board. |
| GET | `/api/images` | open | — | `{"images", "known"}`: the imported chip images and the image names boards may use. |
| GET | `/api/firmware` | open | — | `{"images": [{"image", "version", "built", "origin", "label", "fingerprint", "error"}], "release_url", "project_key"}`: each imported chip image and where it came from (`origin` `project`, `own` or `build`; `label` "project release v0.0.2", "your build v0.0.2" or "your build"), where Get firmware fetches from, and the project key's fingerprint (`""`: none, so nothing can be fetched). |
| POST | `/api/build` | session | `{"name": "<catalog board>"}` or `{"board": {...}}`, with `"network": {"address": "192.0.2.52/24", "gateway": "192.0.2.1"}` or `{"address": "dhcp"}` | Builds a flash image for the board with that address and the controller's two public keys in its keys partition (0x1b000). `{"id", "board", "chip", "keys"}` (`keys`: the fingerprints). `422` if it can't be built (no network, "set up your keys first", chip image not imported or exported before the keys partition). |
| POST | `/api/build/keys` | session | `{}` | Write keys only: the keys partition alone. `{"id", "keys", "keys_only": true}`; `/build/{id}/image.bin` is the 8 KB the page writes at 0x1b000 itself (esptool-js: no erase, read back); no ESP Web Tools manifest (`404`). `422` "set up your keys first". |
| GET | `/build/{id}/manifest.json` | open | — | The ESP Web Tools manifest for that build. |
| GET | `/build/{id}/image.bin` | open | — | The flash image (`application/octet-stream`). |

The two `/build/` routes need only the session cookie, not the `X-Session-Token` header.
"session" means any session, as every POST needs; these four POSTs have no further check. The
controller keeps the last 32 or so builds in memory. Flashing from the browser (Web Serial)
works because the dashboard is on localhost, or on HTTPS behind a reverse proxy.

### Backup

| Method | Path | Level | Body | Reply |
|--------|------|-------|------|-------|
| GET | `/api/backup` | login | — | What a backup would hold: `data_dir`, `parts` (each top-level entry, its size, and whether it is included), `format`, `min_passphrase` (12), `last` (when a backup was last made whole, downloaded from the page or by `espdns backup`; absent if never), and `lock` if the fleet lock is held. |
| POST | `/api/backup` | login + password | `{"passphrase"}` or `{"recipient": "age1…"}`, and `"firmware": true` to include `firmware/` | The encrypted backup as a download (`application/octet-stream`). `409` if the fleet lock is held (it doesn't wait). |

Restore is not in the API. Use [`espdns restore`](cli.md#restore) with the controller stopped.

### Jobs

Anything that changes nodes, and a few other long tasks, runs as a job: one at a time, under
the fleet lock, in the action log with the logged-in user, progress live.

| Method | Path | Level | Body / query | Reply |
|--------|------|-------|--------------|-------|
| POST | `/api/jobs` | session | `{"kind": "<kind>", "params": {...}}` | `202` with the job, and `Location: /api/jobs/<id>`. `400` if the params are refused (the job is not queued). `429` if 8 jobs are already waiting. |
| GET | `/api/jobs` | open | — | Every job kept, newest first, without logs. |
| GET | `/api/jobs/{id}` | open | — | One job with its progress log. `404` if unknown. |
| POST | `/api/jobs/{id}/stop` | session | — | Asks the job to stop at its next safe point. `202` with the job. `409` if it already ended. |
| GET | `/api/jobs/{id}/events` | open | `?after=<line>` or `Last-Event-ID` | Server-Sent Events (below). |

A job: `id`, `kind`, `who`, `args`, `state` (`queued`, `running`, `done`, `failed`,
`stopped`), `stopping`, `created`, `started`, `ended`, `error`, `result`, `progress` (one job
only), `lines`, `last`, and `log` (one job only).

Events on `/api/jobs/{id}/events`:

| Event | Data |
|-------|------|
| `state` | The job without its log. First, and at every change of state. |
| `log` | One progress line. The event ID is the line number, so a reconnect resumes after it. |
| `progress` | The job's progress, when it changes. |
| `end` | The job, once it ended and every line was sent. The stream then closes. |

A comment every 15 seconds keeps the connection open.

### Static files

| Method | Path | Level | Reply |
|--------|------|-------|-------|
| GET | `/` and other paths | page | The dashboard's pages. `/login.html`, `/shell.css`, `/common.js` and every other `.js` and `.css` file are public. Other pages need a session once a password is set (else a redirect to `/login.html`). |

## Job kinds

Start each with `POST /api/jobs` and `{"kind": "...", "params": {...}}`. Params refuse unknown
fields.

### Node actions

Each needs the release key in the data directory and a node the controller knows.

| Kind | Params | What it does | Which nodes |
|------|--------|--------------|-------------|
| `identify` | `{"node": "<address>", "seconds": 30}` | Flickers the LED. `seconds`: 1 to 3600, default 30. | Any node the controller knows (mDNS or `settings.json`). |
| `pause` | `{"node", "seconds": 300}` | Turns blocking off for that long. At most a week (604800). `0` resumes. Default 300. | Nodes in `settings.json`. |
| `flush` | `{"node"}` | Drops the node's cached answers. | Nodes in `settings.json`. |
| `revert` | `{"node", "list": "blocklist"}` | Goes back to the older copy the node keeps: `blocklist`, `overrides` or `zones`. | Nodes in `settings.json`. |
| `reboot-pending` | `{"node"}` | A coordinated reboot of a node a release left waiting for one. | Nodes in `settings.json`. |

During a key rotation, these and the fleet jobs sign for each node with the key it trusts:
the new release key (`keys/release.next.pem`) for a node that has it, `release.pem` for the
others.

### Fleet jobs

| Kind | Params | What it does |
|------|--------|--------------|
| `check` | none (`{}` or omitted) | Reads every node (in `settings.json` or found over mDNS) and asks it the DNS checks a rollout asks; then asks each DNS peer. Changes nothing. Result: `{"nodes", "dns_peers", "ok"}`. |
| `blocklist` | `{"list": "<name>", "accept_change": false}` | Compiles one list from its definition into `lists/<name>.bin`. `accept_change` takes a size change over the limit once. |
| `config-push` | `{"node", "config", "dry_run": true}`, then `{"node", "config", "after": "<dry run job ID>"}` | Sends one config file to one node in `settings.json`. The push must name a dry run of the same node and file that passed in the last 15 minutes, with the file unchanged. |
| `rollout` | A [rollout request](#rollout-request) with `"dry_run": true`, then the same with `"after": "<dry run job ID>"` | The rolling push, one node at a time. The push must name a passing dry run of the same request with the same file contents, from the last 15 minutes. Each dry run serves one push. |
| `apply` | `{"canary": "<node>", "soak_s": 60, "accept_change": ["<list>"]}`, all optional | Sends every pending change as one safe rolling change (below). |
| `fetch-release` | `{"version": "0.0.3", "images": ["esp32s3-eth"]}`, both optional (`version`: a firmware version or its release tag, `firmware-v0.0.3`; default: the newest published; `images` default: the chip images the nodes in `settings.json` need, from their boards; if one has no status yet, the chip images already imported too, which the job log says) | Get firmware: downloads a published release's `SHA256SUMS` and its signature from `settings.json`'s `release_url` (else the controller's `-release-url`, GitHub), checks the signature with the project's key (`/keys/project.pub`), then downloads only the chip images asked for, checks each, and imports them, as `espdns release fetch`. Other imported chip images stay as they are. A chip image the release doesn't have fails the job, naming the ones it has. Not queued without the project's key, with an `images` name that isn't a chip image's, or when nothing is needed ("Nothing to get yet…"). Install over USB sends the chosen board's image; the account menu sends none. Result: `{"version", "images", "label", "fingerprint", "from"}`. |
| `key-rotate` | `{"id": "<rotation>", "nodes": ["<address>"]}` (`nodes` optional: only those this time) | Pushes a signed key rotation, as `espdns key rotate push`: one node at a time, its KEYS release, then its stored config, hosted zones, overrides and blocklist signed again with the new key, then its health; stops at the first failure (run it again to go on); swaps the controller's keys when every listed node has the new ones. Not queued unless every node still to take its release has it signed. Progress and result: the rotation. |
| `refresh` | `{"lists": ["<name>"], "accept_change": false}` | Sends downloaded lists whose sources changed, as an apply of them alone ([below](#the-refresh)). The schedule starts it (by `""`, the controller); `accept_change` takes a size change over the limit once. |

### Adoption jobs

| Kind | Params | What it does |
|------|--------|--------------|
| `adopt` | An [adoption request](#adoption-request) with `"dry_run": true`, then the same with `"after": "<dry run job ID>"` | Adopts a node. The adoption must follow a passing dry run of the same request and files, from the last 15 minutes. One adoption per dry run. |
| `settings-add` | `{"node": "<address>"}` | Adds a node the controller found, already adopted, to `settings.json`. |

### Service jobs

The node page's **Take out of service**, **Return to service** and **Remove from the group**
([operations.md](../operations.md#taking-a-node-out-of-service)), as `espdns node out`, `in`
and `remove`. Each is for a node in `settings.json`.

| Kind | Params | What it does |
|------|--------|--------------|
| `node-out` | `{"node": "<address>"}` | Adds it to `settings.json`'s `out_of_service`. Works while the node is offline. From then on an apply, a rollout, a refresh, a config push and the fleet check skip it and say so (`<address>: skipped: out of service`), and it isn't counted for the last-healthy-node rule. Refused while a key rotation under way includes it. |
| `node-in` | `{"node": "<address>"}` | Returns it to service. It must answer and trust the controller's release key; else it fails and the node stays out. Then it's sent, alone, what it missed: its config, the hosted zones, the overrides, its blocklist, and the newest firmware if it runs older and its chip image of that version is here (else the result says to update it from Nodes). Each is a dry run first, then the push, as an apply's (progress and result as an apply's). Only once all are done is it in service again. Needs the release key. |
| `node-remove` | `{"node": "<address>"}` | Takes it out of `settings.json` (`nodes`, `out_of_service`, and `canary` if it was). Its config file is kept and the node isn't touched. Works while the node is offline. Not queued while another job names it or changes every node (an apply, a refresh, a key rotation), while a key rotation under way includes it, or while a pending firmware update names it. |

`/api/nodes` marks a node out of service with `"out_of_service": true`; `/api/updates` too,
and offers it no update.

### Rollout request

The body of `POST /api/push/preview` and the params of a `rollout` job:

| Field | Meaning |
|-------|---------|
| `kind` | `firmware`, `config`, `blocklist`, `overrides` or `zones`. |
| `nodes` | The nodes to change, from `settings.json`, in order after the canary. Required. |
| `canary` | One of `nodes`. Empty: `settings.json`'s `canary` if it is one of them, else the first. |
| `soak_s` | How long each node is watched before the next. `0`: the default (60). Never less than the default; at most 3600. |
| `firmware` | `firmware`: `builds/<board>` or `images/<image>`, one per chip image. |
| `configs` | `config`: `{"<node>": "<file in configs/>"}`, one for each node. |
| `file` | `blocklist` or `overrides`: a file in `lists/`. |
| `zones` | `zones`: files in `zones/`. |
| `check_blocked` | Names the blocklist must block after the change. |
| `must_resolve` | A file in `lists/` of names that must resolve. |
| `dry_run` | `true` for the dry run. |
| `after` | The push: the dry run's job ID. |

The page's push keeps every safety rule of the CLI: no force, no single-node override, no
skipped DNS checks, no reinstall, `settings.json`'s nodes only.

### Adoption request

The body of `POST /api/adopt/preview` and the params of an `adopt` job:

| Field | Meaning |
|-------|---------|
| `node` | The node's address now. Required. |
| `node_id` | Its ID as shown (its MAC, `aa:bb:cc:dd:ee:ff`). Required. |
| `config` | A file in `configs/`. Required. |
| `address` | `""` (the config's, else the one it runs on), `a.b.c.d/nn`, or `"dhcp"` (never on a network in `no_dhcp`). |
| `gateway` | With a static address. |
| `static` | As + Add node sends it: a fixed `address` with its `gateway` (an address in the node's own network), or `"dhcp"` with `reserved`; never left out. |
| `reserved` | With `dhcp`: the address is reserved for the node in your DHCP server. |
| `add_to_settings` | Once adopted, add its address to `settings.json`. |
| `dry_run` | `true` for the dry run. |
| `after` | The adoption: the dry run's job ID. |

Only a node that says it isn't adopted, under an ID no other known node has, can be adopted.

### The apply

An `apply` job sends every pending change in five steps:

1. check: the changes against the files and each other; every node in `settings.json` read;
   a firmware update's chip image of the newest release downloaded and checked if it isn't
   here (as Get firmware).
2. write: the files written (each replaced version kept in its history).
3. compile: the blocklists the changes touch compiled again.
4. dry run: every rollout checked against every node it goes to, before any node is touched.
5. push: the rollouts one after another (configs, hosted zones, overrides, blocklists,
   firmware), each one node at a time, canary first.

Params, all optional: `canary` (one of `settings.json`'s nodes; default its `canary`),
`soak_s` (60 to 3600), `accept_change` (lists whose size change is taken this once).

If a blocklist's size changed more than allowed, the apply ends `stopped` with `questions` in
its result and pushes nothing. Apply again with the params a question names to take the
change.

If an apply stops part way, the changes stay pending (marked written once their files are).
Applying again finishes them; discarding puts the files back.

### The refresh

On the schedule `settings.json`'s `blocklist_refresh` sets (daily by default), the controller
checks each list with a URL source, outside any job: a conditional GET (`If-None-Match`,
`If-Modified-Since`) of each URL source, the body otherwise only hashed and compared with the
version the nodes run (kept in `blocking/refresh.json`).

- Unchanged: marked checked; nothing in the action log.
- Changed: starts a `refresh` job: the apply's check, compile, dry run and push
  steps for that list alone, with no pending change and no file written.
- Size change over the limit: the job ends `failed` (`refused by the size-change check:
  ...`), nothing pushed, and the schedule doesn't try that version again.
- A list a pending change edits (its definition, or a file it reads) is left to that
  change's apply.
