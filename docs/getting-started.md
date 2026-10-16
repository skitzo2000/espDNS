# Getting started

<img src="img/artie-head.png" alt="Artie" width="64" align="right">

Download one file, start it and open your browser. Setup takes 10 minutes.

espDNS is alpha (0.0.x). Keep a second DNS server around until you trust it.

## What you need

- A Linux computer with Docker, on the same network as your boards.
- Chrome or Edge, to install espDNS on the boards over USB. Usually on that same computer;
  on a server, see [On a server](#on-a-server).
- Two boards from the [catalog](hardware.md), each with a microSD card and a USB data cable
  (not charge-only).
- Two free addresses on your network, outside your router's DHCP pool.

## Start the controller

Make a folder, download one file, and start it:

```sh
mkdir -p -m 700 espdns/data && cd espdns
curl -fsSLO https://github.com/skitzo2000/espDNS/releases/latest/download/compose.yaml
printf 'ESPDNS_UID=%s\nESPDNS_GID=%s\n' "$(id -u)" "$(id -g)" > .env
docker compose up -d
```

Your keys and settings live in `espdns/data`, as your own files. The `.env` line tells the
controller to run as you, so they stay that way. Rather keep them in a Docker volume? The
comments in `compose.yaml` show the swap.

### On a server

The setup below opens the controller at http://127.0.0.1:8480, in a browser on the machine
that runs it. On a server you'll use a browser on another machine instead, and that is the
machine you plug the boards into to install them over USB. Two ways to get there:

- **An SSH tunnel.** Nothing to change on the server. From your own machine run
  `ssh -N -L 8480:127.0.0.1:8480 <server>`, leave it running, and open
  http://127.0.0.1:8480 there.
- **Your HTTPS reverse proxy.** If you already run one on your network, it can serve the
  controller at a name of yours, such as `https://espdns.example.net`, to any browser.
  Set `ESPDNS_URL`, `ESPDNS_PROXY` and `ESPDNS_LISTEN` in `.env`
  ([Behind a reverse proxy](security.md#behind-a-reverse-proxy)). The first password can't
  be set through the proxy: set it on the server, in the `espdns` folder, before you open
  the page:

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller passwd -data /data
  ```

On a server, run the controller as a system user of its own rather than your login user:
make one (for example `sudo useradd --system --no-create-home espdns`), make it the owner of
the `data` folder (`sudo chown espdns: data`), and put its ids in `.env` (`id -u espdns`,
`id -g espdns`) instead of yours.

Two controllers on one machine need different ports (`ESPDNS_LISTEN=127.0.0.1:8481` for the
second): the browser keeps their login cookies apart by port.

## Run the setup

Open http://127.0.0.1:8480 in Chrome or Edge on the same machine. Artie walks you
through it:

1. **Set a password.** At least 12 characters.
2. **Make your keys.** Two clicks. Your nodes only take changes signed with your keys, so
   nobody else can touch them. Your browser downloads `recovery.pem`: save it somewhere
   safe, off this computer.
3. **Add your first node.** Two steps. First install it: press **Install over USB**, pick
   your board, give it its address and gateway, plug it in over USB and press **Install**.
   The controller fetches the firmware for you and checks its signature. Once the board
   answers on your network, press **Add it now**, give it a name, and press **Add node**.
   A node already on your network skips the install: press **+ Add node** and pick it.
   Installing erases the board's flash, not its microSD card. Installing a
   board again with new keys? At its first boot it deletes the old blocklist, allowed and
   blocked sites and hosted zones from its card, since they can't be checked any more
   ([what it deletes](hardware.md#what-espdns-keeps-on-the-card-and-what-it-deletes)).
   The Blocklists page shows **No list yet** for it until you apply your lists and zones again.
4. **Add a second node.** Same again, with the second address. Two nodes keep DNS
   answering while one restarts or updates.
5. **Add your zone.** Optional. Type a name for your network, like `home.example`, and
   press **Host it here**: your nodes answer for it, and your devices get names in it
   (`nas.home.example`). It starts with the records every zone needs; you add yours on the
   Zones page ([Zones](zones.md)). Already run a DNS server with your zones? Copy them
   from it here, under **Set it up myself**.
6. **Tell your router.** In its DHCP settings, set the DNS servers to your two nodes'
   addresses, and remove any others.
7. **Turn on blocking.** Optional. Pick a list or two to block ads and trackers
   ([Blocking](blocking.md)).

> **Artie's tip:** no USB port to pick from? Try another cable, then hold the board's
> BOOT button while you plug it in.

## Check it works

The Nodes page shows both nodes with a green dot. To ask one yourself:

```sh
docker run --rm --network host alpine:3 nslookup example.com 192.0.2.53
```

(Use your node's address.) Then unplug one node and browse: the other keeps answering.

## Keeping it running

- To back up, copy the `espdns/data` folder, or use **Keep the controller safe** in the
  account menu for one encrypted file. Keep `recovery.pem` apart from either.
- To update, download the new `compose.yaml` and start it again, in the same folder:

  ```sh
  curl -fsSLO https://github.com/skitzo2000/espDNS/releases/latest/download/compose.yaml
  docker compose up -d
  ```

  The controller looks for new firmware when it starts and every day. When there's some,
  each node that needs it gets an **Update** button in its row on the Nodes page. Updates go
  one node at a time, so DNS never drops. The nodes don't need the controller to answer, so
  stopping it never stops DNS.

## Where next

- [Zones](zones.md) and [Blocking](blocking.md): what your nodes do.
- [Operations](operations.md): updates, backups and keys in more depth.
- [Troubleshooting](troubleshooting.md): when something's off.
- [Developer guide](developer.md): running from source, the command line, and building
  your own firmware.
