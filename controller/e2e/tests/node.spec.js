// The node page (node.html, node.js, nodeview.js, update.js) against a fake controller:
// serve.sh's controller logs in and serves the pages, and the APIs the page reads are
// answered here (page.route), with documentation addresses (RFC 5737), so nothing goes on the
// network. E2E_SHOTS=<dir> also takes the page and its panels at 375 and 1366 px, light and
// dark, into that directory.
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login } = require("./session");
const { fakeEsp } = require("./fakeboard");

async function watch(page) {
  const seen = [];
  await page.addInitScript(() => {
    window.__csp = [];
    document.addEventListener("securitypolicyviolation",
      e => window.__csp.push(`${e.violatedDirective} blocked ${e.blockedURI || "inline"} (${e.sourceFile}:${e.lineNumber})`));
  });
  page.on("console", m => { if (/Content.Security.Policy/i.test(m.text())) seen.push(m.text()); });
  page.on("pageerror", e => seen.push(`uncaught: ${e.message}`));
  return async () => [...seen, ...await page.evaluate(() => window.__csp)];
}

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54";
const ID2 = "aa:bb:cc:00:00:02", ID3 = "aa:bb:cc:00:00:03";
const MAC2 = "aa:bb:cc:00:00:05", MAC3 = "aa:bb:cc:00:00:06";
const series = vs => Array(60 - vs.length).fill(null).concat(vs);

function node(addr, id, name, board, health, extra = {}, status = {}) {
  return {
    id, addr, hostname: `espdns-${id.slice(-5).replace(":", "")}.local`, source: "settings", online: true,
    last_seen: new Date().toISOString(), polled: new Date().toISOString(), qps: 0.4,
    status: {
      node_id: id, board, version: "0.0.1", elf_sha256: "3fbae0a1c2d3e4f5", uptime_s: 9660, boot_ms: 2300, reset: "software",
      net: { kind: "ethernet", hostname: `${name}.local`, mac: extra.mac }, health, config: { name, source: "node", seq: 7 },
      reboot: { pending: false, reasons: [] },
      zones: [{ name: "local", serial: 2026032134, records: 34, transfers: 12, fails: 0, expired: false }],
      forward_zones: [{ name: "corp.example.net", forwarder: "198.51.100.9" }],
      blocking: { list: { state: "on", entries: 2370238, seq: 5 }, overrides: { state: "on", entries: 3 }, paused_s: 0 },
      services: [{ name: "dns", state: "running" }, { name: "blocking", state: "running" }],
      memory: { internal: { planned: 180000, capacity: 250000 }, psram: { planned: 9000000, capacity: 16000000 } },
      ...status,
    },
    ...extra,
  };
}

function form(name, over = {}) {
  return {
    node: { id: over.id || ID2, host: over.host || DNS2, name, online: true, mac: MAC2, listed: true },
    config: { file: `${name}.json`, exists: true, runs: { state: "file", text: `{\n  "name": "${name}",\n  "network": { "address": "${over.host || DNS2}/24", "gateway": "192.0.2.1" }\n}\n`, seq: 7 } },
    version: "v1",
    settings: {
      name: { value: name, from: "config", applies: "live" },
      network: { value: { address: `${over.host || DNS2}/24`, gateway: "192.0.2.1" }, from: "config", applies: "restart" },
      forwarders: { value: ["198.51.100.1", "198.51.100.2"], from: "config", applies: "live" },
      tz: { value: { tz: "EST5EDT,M3.2.0,M11.1.0", name: "America/New_York" }, from: "config", applies: "live" },
      blocking: { value: true, from: "default", applies: "live" },
      querylog: { value: { enabled: true, client: "full" }, from: "default", applies: "live" },
      led: { value: { led: { kind: "none" }, board: { kind: "none" } }, from: "node", applies: "live" },
    },
    pending: null,
    time_zones: [{ name: "UTC", tz: "UTC0" }, { name: "America/New_York", tz: "EST5EDT,M3.2.0,M11.1.0" }, { name: "Europe/London", tz: "GMT0BST,M3.5.0/1,M10.5.0" }],
    querylog_clients: ["full", "subnet", "hidden"],
  };
}

// fake answers the page's APIs; state.posts is what was sent, state.changes what
// /api/changes lists.
async function fake(page, opts = {}) {
  const state = { changes: [], posts: [], forms: { [ID2]: form("dns2"), [ID3]: form("dns3", { id: ID3, host: DNS3 }) }, jobs: 0 };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/nodes$/, r => json(r, [
    node(DNS2, ID2, "dns2", "p4-ip101", { state: "healthy", answering: true, reasons: [] }, { mac: MAC2 }, opts.pending ? { reboot: { pending: true, reasons: ["config: address"] } } : {}),
    node(DNS3, ID3, "dns3", "ws-s3-eth", { state: "degraded", answering: true, reasons: ["zone refresh failing"] }, { qps: 0, mac: MAC3 },
      { version: "0.0.4", zones: [{ name: "local", serial: 2026032134, records: 34, transfers: 3, fails: 4, expired: false }] }),
  ]));
  await page.route(/\/api\/nodes\/found$/, r => json(r, { nodes: [] }));
  await page.route(/\/api\/nodes\/[^/]+\/settings$/, r => {
    const id = decodeURIComponent(r.request().url().split("/api/nodes/")[1].split("/")[0]);
    const f = state.forms[id];
    if (!f) return json(r, { error: `no node ${id} known` }, 404);
    if (r.request().method() === "GET" && opts.unrecorded === id && !state.written) {
      return json(r, { error: "it runs a config (seq 7) this controller has no copy of, and a node can't send one back: write it here, so a change keeps what it sets",
        write: [{ file: "dns3.json", draft: '{\n  "name": "dns3",\n  "network": {\n    "address": "192.0.2.54/24",\n    "gateway": "192.0.2.1"\n  }\n}\n' }] }, 409);
    }
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON();
      state.posts.push(["settings", id, b]);
      if (b.settings.name === "dns3") return json(r, { error: "name: 192.0.2.54 is called dns3 already", field: "name" }, 400);
      if (b.settings.led && b.settings.led.green === 11) {
        return json(r, { error: "dns2.json: its status light doesn't fit board p4-ip101: GPIO 11 used twice: SPI2 MOSI and LED green" }, 400);
      }
      const to = b.settings.network && b.settings.network.address && b.settings.network.address.split("/")[0];
      if (to && to !== f.node.host) {
        return json(r, { error: `network: this moves ${f.node.name} to ${to}, which isn't a change to apply: a move is made on its own, now. The node restarts onto its new address and is confirmed there`, field: "network", move: to }, 400);
      }
      const fields = Object.keys(b.settings).filter(k => k !== "reset").concat(b.settings.reset || []);
      const words = { name: "name", network: "address", forwarders: "upstream DNS", tz: "time zone", blocking: "blocking", querylog: "query log", led: "status light" };
      const ch = { id: `c${state.changes.length + 1}`, kind: "config", name: f.config.file, summary: `${f.node.name}: ${fields.map(x => words[x]).join(", ")}`, effect: "Applies live: DNS keeps answering" };
      state.changes.push(ch);
      const next = JSON.parse(JSON.stringify(f));
      if (b.settings.forwarders) next.settings.forwarders.value = b.settings.forwarders;
      if (b.settings.blocking !== undefined) next.settings.blocking.value = b.settings.blocking;
      next.version = "v2";
      next.pending = { changes: [ch], settings: next.settings, fields, change: {}, restart: "no", effect: "Applies live: DNS keeps answering" };
      state.forms[id] = next;
      return json(r, next);
    }
    return json(r, f);
  });
  await page.route(/\/api\/nodes\/[^/]+\/move(\/undo)?$/, r => {
    const b = r.request().postDataJSON();
    const undo = r.request().url().endsWith("/undo");
    state.posts.push([undo ? "move-undo" : "move", b]);
    return json(r, undo ? { file: "dns2.json", hash: "h0" } : { file: "dns2.json", node: DNS2, to: b.settings.network.address.split("/")[0], hash: "h1" });
  });
  await page.route(/\/api\/configs\/[^/]+$/, r => {
    state.posts.push(["config", r.request().url().split("/api/configs/")[1], r.request().postDataJSON()]);
    state.written = true;
    return json(r, { name: "dns3.json", hash: "h2" });
  });
  await page.route(/\/api\/updates$/, r => {
    if (r.request().method() === "POST") { state.posts.push(["updates", r.request().postDataJSON()]); return json(r, { ok: true }); }
    return json(r, { scheme: "semver", builds: [], available: [DNS2], nodes: [
      { host: DNS2, listed: true, online: true, runs: { version: "0.0.1", build: "a1" }, newest: { version: "0.0.4", build: "b4", source: "builds/p4-ip101" }, state: "available", why: "" },
      { host: DNS3, listed: true, online: true, runs: { version: "0.0.4", build: "b4" }, newest: { version: "0.0.4", build: "b4", source: "builds/ws-s3-eth" }, state: "current", why: "" },
    ] });
  });
  await page.route(/\/api\/dashboard$/, r => json(r, { window_s: 3600, bin_s: 60, bins: 60,
    fleet: { now: { qps: 0.4 }, hour: { qps: 0.4 }, qps: series([0.2, 0.3]) },
    nodes: [{ key: ID2, id: ID2, addr: DNS2, name: "dns2", online: true, health: "healthy", reasons: [],
      hour: { qps: 0.4, p50_ms: 7, blocked_share: 0.009, cache_hit_rate: 0.62 }, qps: series([0.2, 0.3, 0.2, 0.4, 0.3, 0.5, 0.4, 0.6, 0.5, 0.4]) }] }));
  await page.route(/\/api\/changes$/, r => json(r, { changes: state.changes, restarts: [], apply: null }));
  await page.route(/\/api\/key$/, r => json(r, { source: "file", present: true, fingerprint: "0123456789abcdef" }));
  await page.route(/\/api\/actions(\?.*)?$/, r => json(r, [
    { time: new Date(Date.now() - 3600e3).toISOString(), event: "end", id: "j0", source: "controller", who: "admin", action: "job identify", args: [`{"node":"${DNS2}","seconds":30}`], result: "ok" },
    { time: new Date(Date.now() - 7200e3).toISOString(), event: "end", id: "j9", source: "controller", who: "admin", action: "job flush", args: [`{"node":"${DNS3}"}`], result: "ok" },
  ]));
  await page.route(/\/api\/boards$/, r => json(r, [{ board: { name: "p4-ip101", title: "Guition ESP32-P4" } }, { board: { name: "ws-s3-eth", title: "Waveshare ESP32-S3-ETH" } }]));
  await page.route(/\/api\/jobs$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["job", b]);
    state.jobs++;
    return json(r, { id: `j${state.jobs}`, kind: b.kind, who: "admin", state: "running", created: new Date().toISOString() }, 201);
  });
  await page.route(/\/api\/jobs\/j\d+\/events/, async r => {
    const id = r.request().url().match(/jobs\/(j\d+)\//)[1];
    const job = state.posts.filter(p => p[0] === "job")[Number(id.slice(1)) - 1][1];
    if (opts.slowJobs) await new Promise(res => setTimeout(res, 1500));
    const end = { id, kind: job.kind, who: "admin", state: opts.failJobs ? "failed" : "done", error: opts.failJobs ? `${DNS2}: not rebooted: it is the last healthy node` : "" };
    return r.fulfill({ status: 200, contentType: "text/event-stream",
      body: `id: 1\nevent: log\ndata: ${JSON.stringify({ text: `${DNS2}: restarting it, coordinated` })}\n\nid: 2\nevent: end\ndata: ${JSON.stringify(end)}\n\n` });
  });
  return state;
}

const open = async (page, id) => { await page.goto(`node.html#${encodeURIComponent(id)}`); await expect(page.locator("#name")).not.toHaveText("Reading the node…"); };

test("the node page: identity, state, address and MAC, update as versions, metrics", async ({ page, context }) => {
  const violations = await watch(page);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page);
  await fake(page);
  await open(page, ID2);
  await expect(page.locator("#name")).toHaveText("dns2");
  await expect(page).toHaveTitle(/dns2/);
  const nav = page.locator('nav[aria-label="Main"] a');
  await expect(nav.first()).toHaveAttribute("aria-current", "page");
  const sum = page.locator("#summary");
  await expect(sum.locator(".led")).toHaveClass(/\bhealthy\b/);
  await expect(sum).toContainText("Healthy");
  await expect(sum.locator(".ipc:not(.mac) .ip")).toHaveText(DNS2);
  await expect(sum.locator(".mac .ip")).toHaveText(MAC2);
  await expect(sum).toContainText("Guition ESP32-P4 · Wired · up 2 h 41 m · firmware 0.0.1");
  await expect(page.locator("#why")).toHaveText("Update available: 0.0.1 → 0.0.4");
  await sum.getByRole("button", { name: `Copy ${DNS2}` }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(DNS2);
  await sum.getByRole("button", { name: `Copy MAC ${MAC2}` }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(MAC2);
  await expect(page.locator("#attention")).toBeHidden();
  await expect(page.getByRole("button", { name: "Update firmware" })).toBeVisible();
  // What it serves, the last hour, recent changes, the details.
  await expect(page.locator("#serves")).toContainText("1 copied from your primary, all up to date · 1 forwarded");
  await expect(page.locator("#serves")).toContainText("On · 2,370,238 domains");
  await expect(page.locator("#serves .meter")).toHaveCount(2);
  await expect(page.locator("#minis .mini")).toHaveCount(4);
  await expect(page.locator("#minis .spark")).toHaveCount(1);
  await expect(page.locator("#minis")).toContainText("7 ms typical");
  await expect(page.locator("#recent-val")).toContainText("Light blinked");
  await page.locator("#details > summary").click();
  await expect(page.locator("#details")).toContainText("aa:bb:cc:00:00:02");
  await expect(page.locator("#details")).toContainText("build 3fbae0a1");
  await expect(page.locator("#details")).toContainText(`espdns reboot -host ${DNS2}`);
  await expect(page.locator("#raw-config")).toContainText('"name": "dns2"');
  await expect(page.locator("main")).not.toContainText(/\bnull\b|undefined|NaN/);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Nodes opens a node's page", async ({ page }) => {
  await login(page);
  await fake(page);
  await page.goto("./");
  await page.locator("#nodes .r[data-href]").nth(1).locator(".flag").click();
  await expect(page).toHaveURL(new RegExp(`node\\.html#${encodeURIComponent(ID3)}$`));
  await expect(page.locator("#name")).toHaveText("dns3");
});

test("a degraded node says why, in words, with the fix", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await open(page, ID3);
  await expect(page.locator("#summary .led")).toHaveClass(/\bdegraded\b/);
  const att = page.locator("#attention");
  await expect(att).toBeVisible();
  await expect(att).toHaveClass(/\balert\b/);
  await expect(att).toContainText("dns3 is degraded: zone refresh failing.");
  await expect(att).toContainText("Check that your primary allows this node to copy the zone");
  await expect(att).toContainText("still answers DNS queries meanwhile");
  // A zone's trouble is fixed on Zones: a click away.
  await expect(att.getByRole("link", { name: "Open Zones" })).toHaveAttribute("href", "zones.html");
  // Its own query log: the log filtered to it.
  await expect(page.locator("#hour-log")).toHaveAttribute("href", `querylog.html?node=${encodeURIComponent(ID3)}`);
  await expect(page.locator("#serves")).toContainText("Out of date: local (copies failing)");
  await expect(page.getByRole("button", { name: "Update firmware" })).toHaveCount(0);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("settings: a change saved goes into the changes to apply; a refusal shows in its row", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await open(page, ID2);
  const rows = page.locator("#settings details.ex");
  await expect(rows).toHaveCount(7);
  await expect(rows.nth(0).locator("summary")).toContainText("Name");
  await expect(rows.nth(1).locator(".val")).toHaveText(`${DNS2}/24 · gateway 192.0.2.1`);
  await expect(rows.nth(2).locator(".val")).toHaveText("198.51.100.1, 198.51.100.2");
  await expect(rows.nth(3).locator(".val")).toHaveText("America/New_York");
  await expect(rows.nth(4).locator(".val")).toHaveText("On");
  await expect(rows.nth(5).locator(".val")).toHaveText("Keeps client addresses");
  await expect(rows.nth(6).locator(".val")).toHaveText("None (its board's)");

  // Nothing changed: said, nothing sent.
  await page.locator("#set-save").click();
  await expect(page.locator("#settings")).toContainText("Nothing changed");
  expect(state.posts.filter(p => p[0] === "settings")).toEqual([]);

  // Upstream DNS and blocking changed: one POST with those two, on the version read.
  await rows.nth(2).locator("summary").click();
  await page.locator("#set-fwd").fill("198.51.100.9, 198.51.100.1");
  await rows.nth(4).locator("summary").click();
  await page.locator("#set-blocking").selectOption("off");
  await page.locator("#set-save").click();
  await expect(page.locator("#toast")).toContainText("Added to changes to apply: dns2: upstream DNS, blocking");
  const sent = state.posts.filter(p => p[0] === "settings");
  expect(sent).toHaveLength(1);
  expect(sent[0][1]).toBe(ID2);
  expect(sent[0][2]).toEqual({ settings: { forwarders: ["198.51.100.9", "198.51.100.1"], blocking: false }, version: "v1" });
  await expect(page.locator(".changes .chip-btn")).toHaveText("1 change to apply");
  await expect(page.locator(".pending-note")).toContainText("dns2: upstream DNS, blocking");
  await expect(page.locator("#settings details.ex").nth(2).locator(".val")).toContainText("198.51.100.9, 198.51.100.1");
  await expect(page.locator("#settings details.ex.pending")).toHaveCount(2);

  // A name another node has: refused, shown in the Name row, opened.
  await page.locator("#settings details.ex").nth(0).locator("summary").click();
  await page.locator("#set-name").fill("dns3");
  await page.locator("#set-save").click();
  const nameRow = page.locator('#settings details.ex[data-field="name"]');
  await expect(nameRow).toHaveAttribute("open", "");
  await expect(nameRow.locator(".hint.bad")).toHaveText("name: 192.0.2.54 is called dns3 already");
  expect(state.posts.filter(p => p[0] === "settings")[1][2]).toEqual({ settings: { name: "dns3" }, version: "v2" });
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("settings: the status light, an RGB LED on three GPIOs; a pin taken shows in its row", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await open(page, ID2);
  const row = page.locator('#settings details.ex[data-field="led"]');
  await row.locator("summary").click();
  await expect(row).toContainText("Its board's: None.");
  // Only the pins its kind takes are shown.
  await expect(page.locator("#set-led-pin")).toBeHidden();
  await expect(page.locator("#set-led-red")).toBeHidden();
  await page.locator("#set-led").selectOption("gpio");
  await expect(page.locator("#set-led-pin")).toBeVisible();
  await expect(page.locator("#set-led-low")).toBeVisible();
  await page.locator("#set-led").selectOption("rgb");
  await expect(page.locator("#set-led-pin")).toBeHidden();
  await page.locator("#set-led-red").fill("4");
  await page.locator("#set-led-green").fill("11");
  await page.locator("#set-led-blue").fill("6");
  await page.locator("#set-led-ca").check();
  // A pin the board uses: refused, shown in the row.
  await page.locator("#set-save").click();
  await expect(row.locator(".hint.bad")).toContainText("GPIO 11 used twice: SPI2 MOSI and LED green");
  await page.locator("#set-led-green").fill("5");
  await page.locator("#set-save").click();
  await expect(page.locator("#toast")).toContainText("Added to changes to apply: dns2: status light");
  const sent = state.posts.filter(p => p[0] === "settings");
  expect(sent.at(-1)[2]).toEqual({ settings: { led: { kind: "rgb", red: 4, green: 5, blue: 6, common_anode: true } }, version: "v1" });
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Blink its light, Restart, Update firmware and the quick fixes run as jobs", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, { slowJobs: true });
  await open(page, ID2);
  const jobs = () => state.posts.filter(p => p[0] === "job").map(p => p[1]);

  await page.getByRole("button", { name: "Blink its light" }).click();
  await expect(page.locator("#summary .led")).toHaveClass(/\bidentify\b/);
  await expect(page.locator("#toast")).toContainText("flickers white for 30 seconds");
  await expect.poll(jobs).toEqual([{ kind: "identify", params: { node: DNS2, seconds: 30 } }]);

  await page.getByRole("button", { name: "Restart", exact: true }).click();
  const d = page.getByRole("dialog", { name: "Restart dns2?" });
  await expect(d).toContainText("dns3 keeps answering meanwhile");
  await d.getByRole("button", { name: "Restart", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Restarting dns2" })).toBeVisible();
  await expect(page.getByRole("dialog", { name: "dns2 restarted" })).toContainText("back and answering");
  expect(jobs()[1]).toEqual({ kind: "reboot", params: { node: DNS2 } });
  await page.getByRole("dialog").getByRole("button", { name: "Done" }).click();

  await page.getByRole("button", { name: "Update firmware" }).click();
  const u = page.getByRole("dialog", { name: "Update firmware" });
  await expect(u).toContainText("0.0.1 → 0.0.4");
  await expect(u).toContainText("dns3 keeps answering while dns2 restarts");
  await u.getByRole("button", { name: "Add to changes" }).click();
  await expect(page.locator("#toast")).toContainText("Added to changes to apply: update dns2");
  expect(state.posts.find(p => p[0] === "updates")[1]).toEqual({ nodes: [DNS2] });

  await page.locator("#details > summary").click();
  await page.getByRole("button", { name: "Older blocklist" }).click();
  const rv = page.getByRole("dialog", { name: "Go back to the older blocklist?" });
  await rv.getByRole("button", { name: "Go back" }).click();
  await expect(rv).toHaveCount(0);
  await page.getByRole("button", { name: "Flush its cache" }).click();
  await expect.poll(() => jobs().slice(2)).toEqual([{ kind: "revert", params: { node: DNS2, list: "blocklist" } }, { kind: "flush", params: { node: DNS2 } }]);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a restart refused says why; a reboot pending offers Restart now", async ({ page }) => {
  await login(page);
  await fake(page, { failJobs: true, pending: true });
  await open(page, ID2);
  const att = page.locator("#attention");
  await expect(att).toContainText("A change waits for a restart");
  await att.getByRole("button", { name: "Restart now" }).click();
  const d = page.getByRole("dialog");
  await expect(d).toContainText("A change waits for this restart (config: address)");
  await d.getByRole("button", { name: "Restart", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "dns2 wasn't restarted" })).toContainText("last healthy node");
});

// Reinstall over USB: Install over USB on this page, on the node's board, with the address
// and gateway it runs on; offline too.
test("Reinstall over USB opens Install over USB on the node's board and address", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.route(/\/api\/nodes$/, r => r.fulfill({ json: [{ ...node(DNS2, ID2, "dns2", "p4-ip101", { state: "healthy", answering: true, reasons: [] }, { mac: MAC2 },
    { config: { name: "dns2", source: "node", seq: 7, address: "static", ip: `${DNS2}/24`, gateway: "192.0.2.1" } }), online: false }] }));
  await open(page, ID2);
  const b = page.locator("#acts").getByRole("button", { name: "Reinstall over USB" });
  await expect(b).toBeEnabled();
  await b.click();
  const u = page.getByRole("dialog", { name: "Reinstall dns2 over USB" });
  await expect(u).toBeVisible();
  await expect(u.locator("#usb-board")).toHaveValue("p4-ip101");
  await expect(u.locator("#usb-addr")).toHaveValue(`${DNS2}/24`);
  await expect(u.locator("#usb-gw")).toHaveValue("192.0.2.1");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// A node of yours on DHCP isn't among the nodes found (it's listed): once reinstalled, it is
// waited for at the address it has. Listed isn't back: on USB only, it says to plug its cable
// in, and its console's "no link" is said at once; it is back once it answers there,
// restarted since the install, with nothing to add.
test("Reinstall over USB on DHCP: waited for at its address until it answers, back on Nodes as before", async ({ page }) => {
  const violations = await watch(page);
  await page.addInitScript(fakeEsp, { bootLog: ["ESP-ROM:esp32s3-20210327", "\x1b[0;32mI (311) espdns: espDNS 0.0.5 starting\x1b[0m",
    '\x1b[0;33mW (5342) espdns: health: "state":"no network","answering":false,"reasons":["no link"]\x1b[0m'] });
  await login(page);
  await fake(page);
  const lookups = [];
  let lookup = { address: DNS2, listed: true, found: false, answered: false, error: `${DNS2}: no answer` };
  await page.route(/\/api\/nodes$/, r => r.fulfill({ json: [node(DNS2, ID2, "dns2", "ws-s3-eth", { state: "healthy", answering: true, reasons: [] }, { mac: MAC2 },
    { config: { name: "dns2", source: "node", seq: 7, address: "dhcp" } })] }));
  await page.route(/\/api\/key$/, r => r.fulfill({ json: { present: true, fingerprint: "a1b2c3d4e5f60718", recovery: { present: true, fingerprint: "0f1e2d3c4b5a6978" } } }));
  await page.route(/\/api\/boards$/, async r => {
    const res = await r.fetch();
    await r.fulfill({ response: res, json: (await res.json()).map(e => ({ ...e, image_ready: true })) });
  });
  await page.route(/\/api\/build$/, async r => {
    const board = { name: "ws-s3-eth", title: "Waveshare ESP32-S3-ETH", image: "esp32s3-eth" };
    await r.fulfill({ json: { id: "e2e-ws-s3-eth", board, wifi: false, chip: { image: board.image, chip_family: "ESP32-S3", version: "0.0.5" },
      keys: { release: "a1b2c3d4e5f60718", recovery: "0f1e2d3c4b5a6978" } } });
  });
  await page.route(/\/build\/e2e-ws-s3-eth\/image\.bin$/, r => r.fulfill({ status: 200, contentType: "application/octet-stream", body: Buffer.alloc(0x4000, 0x5a) }));
  await page.route(/\/api\/nodes\/lookup$/, r => { lookups.push(r.request().postDataJSON().address); return r.fulfill({ json: lookup }); });
  await open(page, ID2);
  await page.locator("#acts").getByRole("button", { name: "Reinstall over USB" }).click();
  const u = page.getByRole("dialog", { name: "Reinstall dns2 over USB" });
  await expect(u.locator("#usb-addr")).toHaveValue("dhcp");
  await u.locator("#usb-prepare").click();
  await expect(u.locator("#usb-go")).toBeVisible();
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-installed")).toHaveText("Installed. Plug its network cable in: it's connected to this computer over USB only. " +
    "It can stay on USB for power, or be unplugged and powered where its cable is.", { timeout: 20000 });
  await expect(u.locator("#usb-step-net .hint")).toHaveText("Plug its network cable in.");
  await expect(u.locator("#usb-nolink")).toHaveText("The board reports no network link. Plug its network cable in.");
  await expect.poll(() => lookups.length, { timeout: 10000 }).toBeGreaterThan(1);
  await expect(u.locator("#usb-waiting")).toHaveText(`Waiting for it at ${DNS2}…`);
  await expect(u.locator("#usb-arrived")).toHaveCount(0);
  await expect(u.locator("#usb-step-net")).not.toHaveClass("done");
  // It answers there, restarted since the install: back.
  lookup = { address: DNS2, listed: true, found: true, uptime_s: 0, node: { host: DNS2, id: ID2, board: "ws-s3-eth", listed: true, next: "" } };
  await expect(u.locator("#usb-arrived")).toHaveText(`It's back on your network at ${DNS2}, and on Nodes as before.`, { timeout: 10000 });
  await expect(u.locator("#usb-step-net .hint")).toHaveText(`At ${DNS2}`);
  await expect(u.locator("#usb-add")).toHaveCount(0);
  expect(lookups[0]).toBe(DNS2);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a node not known, and a node not answering", async ({ page }) => {
  await login(page);
  await fake(page);
  await open(page, "aa:bb:cc:00:00:99");
  await expect(page.locator("#name")).toHaveText("No such node");
  await expect(page.locator("#settings-sec")).toBeHidden();
  await page.route(/\/api\/nodes$/, r => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([
    { ...node(DNS2, ID2, "dns2", "p4-ip101", { state: "healthy", answering: true, reasons: [] }), online: false, last_seen: new Date(Date.now() - 240e3).toISOString(), error: "timeout" }]) }));
  await page.goto("about:blank");
  await open(page, ID2);
  await expect(page.locator("#summary .led")).toHaveClass(/\bfault\b/);
  await expect(page.locator("#attention")).toContainText("dns2 isn't answering: no reply for 4 min.");
  await expect(page.getByRole("button", { name: "Restart", exact: true })).toBeDisabled();
  await expect(page.locator("#acts-why")).toHaveText("It isn't answering: it can still be taken out of service, or removed.");
  await expect(page.getByRole("button", { name: "Take out of service" })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Remove from the group" })).toBeEnabled();
});

test("a node not added yet, and one listed but not adopted: no Restart, no signed actions", async ({ page }) => {
  await login(page);
  await fake(page);
  const healthy = { state: "healthy", answering: true, reasons: [] };
  await page.route(/\/api\/nodes$/, r => r.fulfill({ json: [
    node(DNS2, ID2, "dns2", "p4-ip101", healthy, { mac: MAC2 }, { config: { name: "dns2", source: "defaults", seq: 0 }, reboot: { pending: true, reasons: ["config: address"] } }),
    node(DNS3, ID3, "dns3", "ws-s3-eth", healthy, { source: "mdns", mac: MAC3 }, { config: { name: "dns3", source: "defaults", seq: 0 } })] }));

  // Found on the network, not added: Set it up; its light may blink, nothing else.
  await open(page, ID3);
  await expect(page.locator("#attention")).toContainText("dns3 isn't one of your nodes yet.");
  await expect(page.locator("#attention").getByRole("link", { name: "Set it up" })).toBeVisible();
  await expect(page.locator("#acts button")).toHaveText(["Blink its light"]);
  await expect(page.getByRole("button", { name: "Flush its cache" })).toBeHidden();

  // Listed, but on its board's defaults (reinstalled over USB): reinstall, take out of
  // service or remove it; no Restart (nor Restart now), no update, no quick fixes.
  await page.goto("about:blank");
  await open(page, ID2);
  await expect(page.locator("#attention")).toContainText("dns2 isn't adopted.");
  await expect(page.locator("#attention")).toContainText("Take it out of service, then return it to service");
  await expect(page.locator("#acts button")).toHaveText(["Blink its light", "Reinstall over USB", "Take out of service", "Remove from the group"]);
  await expect(page.getByRole("button", { name: /^Restart/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Flush its cache" })).toBeHidden();
  await expect(page.locator("#acts-why")).toContainText("It isn't adopted");
});

test("out of service: taken out while offline, muted, returned to service, removed: each behind a confirm, as a job", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  const jobs = () => state.posts.filter(p => p[0] === "job").map(p => p[1]);
  const offline = { ...node(DNS3, ID3, "dns3", "ws-s3-eth", { state: "healthy", answering: true, reasons: [] }), online: false,
    last_seen: new Date(Date.now() - 600e3).toISOString(), error: "timeout" };
  let out = false;
  await page.route(/\/api\/nodes$/, r => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([
    node(DNS2, ID2, "dns2", "p4-ip101", { state: "healthy", answering: true, reasons: [] }), { ...offline, out_of_service: out }]) }));

  // Offline, in service: an alert, and it can be taken out of service.
  await open(page, ID3);
  await expect(page.locator("#attention")).toHaveClass(/\balert\b/);
  await page.getByRole("button", { name: "Take out of service" }).click();
  const d = page.getByRole("dialog", { name: "Take dns3 out of service?" });
  await expect(d).toContainText("gets no changes, and isn't counted while dns2 changes");
  await d.getByRole("button", { name: "Take out of service" }).click();
  await expect(page.getByRole("dialog", { name: "dns3 is out of service" })).toBeVisible();
  expect(jobs()).toEqual([{ kind: "node-out", params: { node: DNS3 } }]);
  await page.getByRole("dialog").getByRole("button", { name: "Done" }).click();

  // Out of service: muted, said plainly, never an alert; Return to service and Remove.
  out = true;
  await page.goto("about:blank");
  await open(page, ID3);
  await expect(page.locator("#summary .led")).toHaveClass(/\bout\b/);
  await expect(page.locator("#summary")).toContainText("Out of service");
  const att = page.locator("#attention");
  await expect(att).toContainText("dns3 is out of service.");
  await expect(att).toContainText("It isn't answering: no reply for 10 min.");
  await expect(att).not.toHaveClass(/\balert\b/);
  await page.getByRole("button", { name: "Return to service" }).click();
  const back = page.getByRole("dialog", { name: "Return dns3 to service?" });
  await expect(back).toContainText("what it missed");
  await back.getByRole("button", { name: "Return to service" }).click();
  await expect(page.getByRole("dialog", { name: "dns3 is back in service" })).toBeVisible();
  expect(jobs()[1]).toEqual({ kind: "node-in", params: { node: DNS3 } });
  await page.getByRole("dialog").getByRole("button", { name: "Done" }).click();

  await page.getByRole("button", { name: "Remove from the group" }).click();
  const rm = page.getByRole("dialog", { name: "Remove dns3 from the group?" });
  await expect(rm).toContainText(`take ${DNS3} off your router's DNS servers`);
  await expect(rm).toContainText("New device found");
  await rm.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "dns3 was removed" })).toBeVisible();
  expect(jobs()[2]).toEqual({ kind: "node-remove", params: { node: DNS3 } });
  await expect(page).toHaveURL(/\/(index\.html)?$/);

  // On Nodes: its row muted with its flag, not counted as a node down.
  await expect(page.locator("#nodes .r.r-out .flag")).toHaveText("Out of service");
  await expect(page.locator("#nodes .r.r-out")).not.toHaveClass(/r-bad|r-warn/);
  await expect(page.locator("#facts")).toContainText("1 of 1 node answering");
  await expect(page.locator("#sentence")).toHaveText("DNS is answering on dns2");
  await expect(page.locator("#copy-dns")).toHaveAttribute("title", DNS2);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a job refused keeps its panel open with why", async ({ page }) => {
  await login(page);
  const state = await fake(page, { failJobs: true });
  await open(page, ID2);
  await page.getByRole("button", { name: "Take out of service" }).click();
  const d = page.getByRole("dialog", { name: "Take dns2 out of service?" });
  await d.getByRole("button", { name: "Take out of service" }).click();
  await expect(d).toContainText("last healthy node");
  expect(state.posts.filter(p => p[0] === "job").map(p => p[1].kind)).toEqual(["node-out"]);
});

test("what a node reports is shown as text, never as markup", async ({ page }) => {
  const errs = await watch(page);
  await login(page);
  await fake(page);
  const evil = '<img src=x id="pwn" onerror="window.__pwned=1">';
  await page.route(/\/api\/nodes$/, r => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([
    node(DNS2, ID2, "dns2", `<b id="pwnb">${evil}</b>`, { state: "degraded", answering: true, reasons: [`listeners failed${evil}`] },
      { hostname: `${evil}.local`, mac: MAC2 }, { config: { name: evil, source: "node", seq: 7, error: evil }, reset: evil, version: evil }),
  ]) }));
  await open(page, ID2);
  await expect(page.locator("#name")).toHaveText(evil);
  await page.locator("#details > summary").click();
  await expect(page.locator("#details")).toContainText(evil);
  await expect(page.locator("#attention")).toContainText(evil);
  await expect(page.locator("#pwn, #pwnb")).toHaveCount(0);
  expect(await page.evaluate(() => window.__pwned)).toBeUndefined();
  expect(await errs()).toEqual([]);
});

test("recent changes are this node's: an address isn't matched as the start of a longer one", async ({ page }) => {
  await login(page);
  await fake(page);
  const at = new Date().toISOString();
  await page.route(/\/api\/actions(\?.*)?$/, r => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([
    { time: at, event: "end", id: "j1", who: "admin", action: "job flush", args: [`{"node":"${DNS2}0"}`], result: "ok" },
    { time: at, event: "end", id: "j2", who: "admin", action: "reboot", args: [`-host=${DNS2}1`], result: "ok" },
    { time: at, event: "end", id: "j3", who: "admin", action: "job identify", args: [`{"node":"${DNS2}","seconds":30}`], result: "ok" },
  ]) }));
  await open(page, ID2);
  await page.locator("#recent > summary").click();
  await expect(page.locator("#recent-list .change")).toHaveCount(1);
  await expect(page.locator("#recent-list")).toContainText("Light blinked");
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: node page, ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      await login(page);
      await fake(page);
      const shot = name => page.screenshot({ path: path.join(shots, `${name}-${width}-${scheme}.png`), fullPage: true });
      await open(page, ID2);
      await expect(page.locator("#minis .spark")).toHaveCount(1);
      await expect(page.locator("#settings details.ex")).toHaveCount(6);
      await shot("node");
      await page.locator("#settings details.ex").nth(1).locator("summary").click();
      await page.locator("#settings details.ex").nth(2).locator("summary").click();
      await page.locator("#details > summary").click();
      await shot("node-open");
      await page.getByRole("button", { name: "Restart", exact: true }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.screenshot({ path: path.join(shots, `node-restart-${width}-${scheme}.png`) });
      await page.keyboard.press("Escape");
      await page.getByRole("button", { name: "Update firmware" }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.screenshot({ path: path.join(shots, `node-update-${width}-${scheme}.png`) });
      await page.keyboard.press("Escape");
      await open(page, ID3);
      await expect(page.locator("#attention")).toBeVisible();
      await shot("node-degraded");
    });
  }
}

test("a new address is a move, made on its own: Move it, a dry run, then the push", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await open(page, ID2);
  const row = page.locator('details[data-field="network"]');
  await row.locator("summary").click();
  await page.locator("#set-addr").fill("192.0.2.60/24");
  await page.locator("#set-save").click();
  await expect(row).toContainText("a move is made on its own");
  await row.getByRole("button", { name: "Move it to 192.0.2.60" }).click();
  const p = page.locator("aside.drawer");
  await expect(p.locator("h2")).toHaveText("Move dns2 to 192.0.2.60?");
  await expect(p).toContainText(`goes back to ${DNS2}`);
  await p.getByRole("button", { name: "Move it" }).click();
  await expect(p.locator("h2")).toHaveText("dns2 moved");
  const posts = state.posts.filter(x => x[0] === "move" || x[0] === "job");
  expect(posts.map(x => x[0] === "move" ? `move ${JSON.stringify(x[1].settings)}` : `${x[1].kind} ${JSON.stringify(x[1].params)}`)).toEqual([
    'move {"network":{"address":"192.0.2.60/24","gateway":"192.0.2.1"}}',
    `config-push {"node":"${DNS2}","config":"dns2.json","dry_run":true}`,
    `config-push {"node":"${DNS2}","config":"dns2.json","after":"j1"}`,
  ]);
  // Not a pending change.
  expect(state.changes).toEqual([]);
  await p.getByRole("button", { name: "Done" }).click();
  expect(await violations()).toEqual([]);
});

test("a move that doesn't go through puts the config file back", async ({ page }) => {
  await login(page);
  const state = await fake(page, { failJobs: true });
  await open(page, ID2);
  await page.locator('details[data-field="network"] summary').click();
  await page.locator("#set-addr").fill("192.0.2.60/24");
  await page.locator("#set-save").click();
  await page.getByRole("button", { name: "Move it to 192.0.2.60" }).click();
  await page.locator("aside.drawer").getByRole("button", { name: "Move it" }).click();
  await expect(page.locator("aside.drawer h2")).toHaveText("dns2 wasn't moved");
  expect(state.posts.filter(x => x[0] === "move-undo").map(x => x[1])).toEqual([{ hash: "h1" }]);
});

test("a node running a config with no copy here: written on its page, a click away", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, { unrecorded: ID3 });
  await open(page, ID3);
  const box = page.locator("#settings");
  await expect(box).toContainText("no copy of");
  await expect(box.locator("summary")).toContainText("Write its config here");
  const text = box.locator("textarea");
  await expect(text).toHaveValue(/"address": "192.0.2.54\/24"/);
  await text.fill('{"name":"dns3","time":{"tz":"UTC0"}}');
  await box.getByRole("button", { name: "Save", exact: true }).click();
  await expect(box.locator("#set-save")).toBeVisible();
  expect(state.posts.filter(x => x[0] === "config")).toEqual([["config", "dns3.json", { text: '{"name":"dns3","time":{"tz":"UTC0"}}', hash: "" }]]);
  expect(await violations()).toEqual([]);
});
