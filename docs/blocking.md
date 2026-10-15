# Blocking

Ad and tracker blocking: building blocklists, sending them to the nodes safely, fixing a
wrong block, and reading the query log. Zones and forwarding are in [zones.md](zones.md);
the big picture is in [Architecture](architecture.md#blocking). espDNS is alpha (0.0.x).

## How it works

The controller downloads and compiles your lists into one compact file, signs it, and
rolls it out one node at a time, canary first. Nodes never download lists. Each keeps the
list it runs and the one before, and keeps blocking with the controller off.

## Before you start

Commands here run from `controller/`. The data directory is the one `ESPDNS_DATA` names in
`controller/.env` (`/data` inside the container). Commands that change a node sign with
`-key /data/keys/release.pem`, the key from the first run's Make your keys
([Getting started](getting-started.md#run-the-setup)); other ways to
give it are in the [CLI reference](reference/cli.md#secrets).

The dashboard is at http://127.0.0.1:8480. Open **Blocking** (the **Blocklists** page). You
need to be logged in to change lists or see the query log.

## How a node decides

For each query, a node checks, in order:

1. Its own zones: hosted and secondary zones are never blocked.
2. The overrides: your own short block and allow list. They apply even while paused.
3. Pause: if blocking is paused, nothing else is blocked.
4. The blocklist: its block entries, then its allow entries.
5. Forwarding: names that pass go to the forwarders. CNAME targets in the answer are
   checked against the blocklist too, so a tracker hiding behind a first-party name is caught.

A blocked name gets `0.0.0.0` for A, `::` for AAAA, and an empty answer (NODATA) for other
types. Blocked answers are never cached. To change this, in the node config:

```json
"blocking": { "answer": "nxdomain", "ttl": 10 }
```

- `answer`: `"null"` (the default, as above) or `"nxdomain"`.
- `ttl`: the TTL of a blocked answer, 0 to 86400 seconds (default 10).
- `"enabled": false` turns the blocking service off on that node, live.

## Lists

A list is a named definition on the Blocklists page. It compiles into one file,
`lists/<name>.bin` in the data directory.

### Source formats

Every source names its format first, as `kind:` before the file or URL. The controller
doesn't try to detect it.

| Kind | What a line looks like | What it blocks |
|---|---|---|
| `hosts` | `0.0.0.0 ads.example.com` | That exact name |
| `domains` | `ads.example.com` | That exact name |
| `wildcard` | `ads.example.com` | The name and all its subdomains |
| `adblock` | `\|\|ads.example.com^` | The name and all its subdomains. `@@\|\|name^` allows them |
| `rpz` | A Response Policy Zone, as a master file | See below |

An RPZ is read the way BIND or Technitium publish it. `CNAME .`, `CNAME *.` and
`CNAME rpz-drop.` block a name; `CNAME rpz-passthru.` allows it; `*.name` covers the name and
its subdomains. What a block or allow list can't express is skipped and counted: local data
(A, AAAA, TXT records), `rpz-tcp-only.`, and IP, client IP and name server triggers.

The compiler also:

- drops public suffixes and top-level domains (such as `com` or `co.uk`);
- removes duplicates and entries already covered by a parent entry;
- never blocks a name on the popular names list by accident (a Tranco CSV with
  `rank,name` lines, or one name per line);
- refuses a build that blocks any name on the must resolve list.

### Where a source comes from

A file source lives in the data directory under `blocking/sources/`. Create and edit them
on the Blocklists page under **Source files** (**New source file**), or upload one with
**Replace from a file**. Names end in `.txt`, `.zone`, `.rpz`, `.csv`, `.hosts` or
`.list`. Each save keeps the previous version. Use files for your own lists and allow lists.

A URL source is fetched at each compile and checked for new versions on a schedule
([Keep the lists up to date](#keep-the-lists-up-to-date)):

- It must be https. Plain http is refused, and so is a redirect from https to http.
- It must be on a public address. Loopback, private, link-local, shared and reserved
  addresses are refused, checked at each connect and after every redirect, so a name that
  later points inside your network is refused too.
- A download can be up to 512 MiB.

### Lists hosted inside your network

To fetch a list from a server on your own network, put that host on the internal
allowlist in `settings.json` in the data directory (`controller/data/settings.json`):

```json
"internal_sources": ["192.0.2.10", "lists.home.arpa"]
```

- Each entry is a host only: a name or an IP address, no scheme, no port.
- A source on that host may use plain http and an inside address.
- The fetch goes to that host only; a redirect to another host is refused.
- With no `internal_sources` (the default), every URL source must be https on a public
  address.

The controller rereads `settings.json` every 10 seconds. An unknown field in it is an error.

### Create a list

1. On the Blocklists page, under **New list**, type a name and click **Create**. The name
   `overrides` is taken.
2. Under **Block (sources)**, add one source per line, such as
   `adblock:https://lists.example.com/pro.txt` or `domains:my-blocks.txt`.
3. Under **Allow (allow lists)**, add sources this list must never block, in the same form.
4. Optionally set **Popular names** and **Must resolve** (files in `blocking/sources/`).
5. Optionally set **Max change %** and **Min change (entries)** for the size-change check
   (below). Leave them empty for the defaults.
6. Click **Save**.

Definitions are stored in `blocking/lists.json`; each save keeps the previous version.

## Compile

Click **Compile**. The controller fetches every source and writes `lists/<name>.bin`. The job
log shows each source with its entry counts, and the page shows the same compile as a
command line.

### The size-change check

A compile is refused, and nothing written, when compared with the list's previous build:

- the blocked or allowed entries change by more than 20% and by more than 100
  entries, or
- the file size changes by more than 20% (between builds of the same format).

This stops a source that failed, moved or was cut short from reaching the nodes unnoticed.
A list's first build is taken at any size.

If the change is meant (you added or dropped a source), click **Accept this change once**.
The next build is compared with that one. `Max change %` set to 0 refuses any change in
size; `Min change` set to 0 removes the floor.

### Compile from the command line

The same compile, with no node touched and no key needed:

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    blocklist -list adblock:https://lists.example.com/pro.txt \
    -list domains:/data/blocking/sources/my-blocks.txt \
    -allow domains:/data/blocking/sources/allow.txt \
    -must-resolve /data/blocking/sources/must-resolve.txt \
    -out /data/lists/main.bin
```

The `-out` directory must already exist; the controller creates `data/lists` the first time
it compiles from the page. Add `-internal <host>` (repeatable) for a list server on the
internal allowlist. `-max-change` and `-min-change` set the size-change limits,
`-accept-change` takes one refused build, and `-json` prints the result as JSON.

## Keep the lists up to date

Lists added by web address change upstream, often daily. The controller checks them on a
schedule and rolls a changed list out by itself, the same careful way an apply does. On the
Blocking page, under the lists:

- **Check for new versions**: Every 6 hours, Every 12 hours, Every day (the default), Every
  week, or Never. Saved at once in `settings.json` as `"blocklist_refresh"`
  ([reference](reference/settings.md#fields)).
- **Refresh now** checks every downloaded list at once.
- Each list's row shows **Updated** (when the version the nodes run arrived) and
  **Checked** (when the controller last asked its server).

A check is cheap: the controller sends `If-None-Match` and `If-Modified-Since`, and only
compares a download's hash with what the nodes run. A changed list shows in Recent changes
as **Blocklists updated to their new versions**.

- If the size-change check refuses a new version, nothing is sent, the page says so in red
  with the numbers, and Recent changes lists **Blocklist update refused**. A big shrink is
  often a failed download; the schedule only tries a newer version. If the change is
  expected, click **Send it anyway** to take it once.
- If a check fails (server down, refused address), the page says why, and it's retried
  within an hour.
- A list your pending changes edit isn't refreshed on its own; applying your change
  downloads it. A refresh never sends your pending changes.

Check times survive a restart (`blocking/refresh.json`), and each list gets a little jitter
(up to a twentieth of the schedule, at most 30 minutes) so the flock doesn't all ask at once.

## Roll out a list

After a compile, click **Open in Push**. The **Push** page opens with the kind and file
chosen.

1. Choose the nodes and the **canary**, the node changed first.
2. Choose the **soak** time: how long each node runs the new list, watched, before the next
   starts. One minute to an hour.
3. Optionally add names the list **must block**, and a must-resolve file
   (`lists/must-resolve*.txt` in the data directory).
4. Click **Dry run**. It checks every node and shows where each list goes (memory or SD
   card, swapped live or at the next reboot).
5. Click **Roll out**. It must follow a passing dry run from the last 15 minutes, with the
   same choices and files.

Each node in turn takes the list, answers real test queries (a local name, a forwarded name,
the must-block and must-resolve names), and is watched for the soak time: still on the new
list, no reboot, no jump in failed or dropped queries. The last node is soaked too.

If any node fails, every node that took the list goes back to its previous copy, failed
node first. Nodes not yet changed aren't touched. If a node can't go back, the page names it
and says what to do next.

From the command line (dry run first, then the same command without `-dry-run`):

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    rollout -kind blocklist -all -data /data -file /data/lists/main.bin \
    -check-blocked ads.example.com -dry-run -key /data/keys/release.pem
```

`-canary <node>` picks the first node. `-soak 5m` sets the soak time.

## Overrides

Overrides are your own small block and allow list, checked before the blocklist. Use them to
fix a wrong block or add a quick one.

- `overrides.txt`: names to block.
- `overrides-allow.txt`: names to allow, even if a list blocks them.

Both are wildcard lists: an entry covers the name and its subdomains. Edit them side by side
under **Your own rules** on the Blocking page (Always allowed, Always blocked). Each change
waits in **changes to apply**; **Apply** compiles and rolls it out like a list. Overrides
still apply while blocking is paused.

> **Artie's tip:** a site broken by a block? Add it to Always allowed rather than editing the
> big list. It's a small push, and it's live.

## Pause blocking

Each node's row on the Blocklists page has **Pause**, from 5 minutes to 1 day. **Resume**
ends it early, and so does a reboot. Overrides keep working.

From the command line (`-for` up to `168h`, a week; `-for 0` resumes):

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    pause -host 192.0.2.53 -for 30m -data /data -key /data/keys/release.pem
```

## Revert

Each node keeps the list before the current one. Click **Revert list** or
**Revert overrides** in a node's row to go back to it. The node switches live, or at its
next reboot if two copies don't fit in its memory. It stays on the older copy until you push
a newer one. If there's nothing older, the node refuses and the page shows why.

From the command line:

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    revert -host 192.0.2.53 -kind blocklist -data /data -key /data/keys/release.pem
```

Use `-kind overrides` for the overrides. To go back further than one copy, push an older
list again.

## What each node runs

The **Nodes** table on the Blocklists page shows each node's list and overrides: sequence
number, entries, memory or SD card tier, whether it reverted, which compiled file it runs,
and whether it's paused.

A node installed again over USB with new keys deletes its old list and allowed and blocked
sites from its SD card at first boot: they can't be checked any more. It shows **No list
yet** until you roll them out to it again
([what it deletes](hardware.md#what-espdns-keeps-on-the-card-and-what-it-deletes)).

How big a list a node can hold depends on its board's memory. A list that doesn't fit is
refused by the node, and the dry run says so.

## The query log

Each node keeps recent queries in its own memory; the size comes from the board.

Each entry has the time, client, query type and name, result, response code and answer
time. The result shows which rule decided a block: `blocked(list)`,
`overridden(override)`, `blocked(cname)`, or `forwarded(allow)` for a name the overrides let
through.

### Privacy

The node config sets how much of each client address the log keeps:

```json
"querylog": { "enabled": true, "client": "subnet" }
```

- `"full"` (the default): the whole address.
- `"subnet"`: only the client's /24 network.
- `"hidden"`: no client address at all.

It applies as each entry is written, so a left-out address is never stored. Made stricter,
it also masks entries already logged, on the node and in the controller; made looser,
nothing comes back. `"enabled": false` turns the log off and frees its memory. Both apply
live.

### Read the log

Open **Query log** in the dashboard. Filter by node, client, name, result and type. It
follows new queries live; **Pause** stops the updates. It needs a login, because it shows
who asked for what.

The controller keeps the newest 20,000 entries across all nodes, in memory only. A
restart starts the buffer over.

To read one node's log directly:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller querylog -host 192.0.2.53 -follow
```

Without `-follow` it prints what the node holds and stops. `-json` prints one JSON object
per query. A node serves its query log only to private addresses.

## Charts

Under the log, the **Query log** page charts the fleet's last hour: queries a second, how
many were blocked, answer time (typical, and the slowest answers' line), and failed answers.
Above them are the most-asked sites and devices (login needed, like the log). The controller
reads each node every 10 seconds and keeps the hour in memory.

Each node also serves its counters at `http://<node>/metrics` in Prometheus text format with
no login, so your own monitoring works with the controller on or off. For one node's summary
(`-raw` prints them as the node sends them):

```sh
docker compose run --rm --entrypoint /espdns espdns-controller metrics -host 192.0.2.53
```

## Quick reference

| Task | Where |
|---|---|
| Add a source file or custom list | Blocklists page, **Source files** |
| Define a list | Blocklists page, **New list** |
| Fetch from a list server inside your network | `settings.json`, `"internal_sources"` |
| Build a list | **Compile**; or `blocklist` |
| Take a refused build | **Accept this change once**; or `-accept-change` |
| How often downloaded lists are checked | Blocking page, **Check for new versions**; or `settings.json`, `"blocklist_refresh"` |
| Check the downloaded lists now | Blocking page, **Refresh now** |
| Take a new version the size-change check refused | Blocking page, **Send it anyway** |
| Send a list to the nodes | **Open in Push**; or `rollout -kind blocklist` |
| Fix one wrong block | **Overrides**, then compile and push `overrides.bin` |
| Pause blocking | **Pause** in the node's row; or `pause` |
| Go back to the previous list | **Revert list**; or `revert -kind blocklist` |
| Hide client addresses in the log | Node config, `"querylog": {"client": "hidden"}` |
