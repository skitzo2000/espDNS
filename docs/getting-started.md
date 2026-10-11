# Getting started

<img src="img/artie-head.png" alt="Artie" width="64" align="right">

Download one file, start it, open your browser. About 15 minutes for two boards.

espDNS is **alpha (0.0.x)**. Keep a second DNS server around until you trust it.

## What you need

- **A Linux computer with Docker**, on the same network as your boards.
- **Chrome or Edge** on that computer. It installs espDNS on the boards over USB.
- **Two boards** from the [catalog](hardware.md), each with a microSD card and a USB data
  cable (not charge-only).
- **Two free addresses** on your network, outside your router's DHCP pool.

## Start the controller

Make a folder, download one file, and start it:

```sh
mkdir -p -m 700 espdns/data && cd espdns
curl -fsSLO https://github.com/skitzo2000/espdns/releases/latest/download/compose.yaml
printf 'ESPDNS_UID=%s\nESPDNS_GID=%s\n' "$(id -u)" "$(id -g)" > .env
docker compose up -d
```

Your keys and settings live in `espdns/data`, as your own files. The `.env` line tells the
controller to run as you, so they stay that way. Rather keep them in a Docker volume? The
comments in `compose.yaml` show the swap.

## Run the setup

Open **http://127.0.0.1:8480** in Chrome or Edge on the same machine. Artie walks you
through seven steps:

1. **Set a password.** At least 12 characters.
2. **Make your keys.** Two clicks. Your nodes only take changes signed with your keys, so
   nobody else can touch them. Your browser downloads `recovery.pem`: save it somewhere
   safe, off this computer.
3. **Zone primary.** Optional. Skip it unless you already run a DNS server with your own
   zones ([Zones](zones.md)).
4. **Add your first node.** Pick your board, give it its address and gateway, plug it in
   over USB and press **Install**. The controller fetches the firmware for you and checks
   its signature. Installing erases the board.
5. **Add a second node.** Same again, with the second address. Two nodes keep DNS
   answering while one restarts or updates.
6. **Tell your router.** In its DHCP settings, set the DNS servers to your two nodes'
   addresses, and remove any others.
7. **Turn on blocking.** Optional. Pick a list or two to block ads and trackers
   ([Blocking](blocking.md)).

> **Artie's tip:** no USB port to pick from? Try another cable, then hold the board's
> BOOT button while you plug it in.

## Check it works

The dashboard shows both nodes, healthy. To ask one yourself:

```sh
docker run --rm --network host alpine:3 nslookup example.com 192.0.2.53
```

(Use your node's address.) Then unplug one node and browse: the other keeps answering.

## Keeping it running

- **Back up.** Copy the `espdns/data` folder, or use **Keep the controller safe** in the
  account menu for one encrypted file. Keep `recovery.pem` apart from either.
- **Update.** Download the new `compose.yaml` and start it again, in the same folder:

  ```sh
  curl -fsSLO https://github.com/skitzo2000/espdns/releases/latest/download/compose.yaml
  docker compose up -d
  ```

  Then update your nodes from the Nodes page. They go one at a time, so DNS never drops.
  The nodes don't need the controller to answer, so stopping it never stops DNS.

## Where next

- [Zones](zones.md) and [Blocking](blocking.md): what your nodes do.
- [Operations](operations.md): updates, backups and keys in more depth.
- [Troubleshooting](troubleshooting.md): when something's off.
- [Developer guide](developer.md): running from source, the command line, and building
  your own firmware.
