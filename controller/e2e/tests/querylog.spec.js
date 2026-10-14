// The Query log page (querylog.html, querylog.js) against a fake controller: serve.sh's
// controller logs in and serves the pages, and the APIs the page reads are answered here
// (page.route), with documentation addresses (RFC 5737) and names (example.com), so nothing
// goes on the network. E2E_SHOTS=<dir> also takes the page, its filters and the changes
// panel after an Allow at 375 and 1366 px, light and dark, into <dir>/querylog.
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login, apiGet } = require("./session");

// Records every CSP violation and uncaught error, as csp.spec.js does.
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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", DNS4 = "192.0.2.55";
const LAPTOP = "192.0.2.10", TABLET = "192.0.2.20", PRINTER = "198.51.100.7";
const K2 = "aa:bb:cc:00:00:02", K3 = "aa:bb:cc:00:00:03", K4 = "aa:bb:cc:00:00:04";

function nodeOf(addr, id, name) {
  return { id, addr, hostname: `${name}.local`, source: "settings", online: true, last_seen: new Date().toISOString(),
    status: { version: "0.0.4", net: { kind: "ethernet", hostname: `${name}.local` }, health: { state: "healthy", reasons: [] }, config: { name } } };
}

const ago = s => new Date(Date.now() - s * 1000).toISOString();
let seq = 0;
function entry(node, client, qname, result, extra = {}) {
  seq++;
  return { id: seq, node, at: ago(100 - seq), seq, uptime_ms: 1000, time: null, client, transport: "udp", qname, qtype: "A",
    result, rcode: "NOERROR", latency_us: result === "forwarded" ? 9100 : 300, ...extra };
}

// fake answers the page's APIs. state.entries is the log (oldest first); state.more are
// entries that arrive on the next follow; state.asked the query logs asked; state.posts what
// was sent.
async function fake(page, opts = {}) {
  seq = 0;
  const state = {
    entries: [
      entry(K2, LAPTOP, "www.example.com", "forwarded"),
      entry(K3, TABLET, "ads.example.net", "blocked", { rule: "list" }),
      entry(K2, LAPTOP, "printer.home.example", "hosted"),
      entry(K3, null, "tracker.example.org", "overridden", { rule: "override" }),
      entry(K2, PRINTER, "cloud.example.org", "servfail", { rcode: "SERVFAIL" }),
      entry(K2, LAPTOP, "www.example.com", "cache"),
      entry(K3, TABLET, "nothere.example.com", "forwarded", { rcode: "NXDOMAIN" }),
    ],
    more: [],
    asked: [],
    posts: [],
    changes: [],
    allowText: "kept.example.com\n",
    nodes: opts.nodes || [
      { key: K2, name: "dns2", addr: DNS2, state: "ok", enabled: true, client: "full", lost: 0 },
      { key: K3, name: "dns3", addr: DNS3, state: "ok", enabled: true, client: "subnet", lost: 12 },
      { key: K4, name: "dns4", addr: DNS4, state: "unsupported", enabled: false, client: "" },
    ],
  };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/nodes$/, r => json(r, [nodeOf(DNS2, K2, "dns2"), nodeOf(DNS3, K3, "dns3"), nodeOf(DNS4, K4, "dns4")]));
  const names = { [LAPTOP]: "laptop.home.example", [PRINTER]: "printer.home.example" };
  const pick = u => {
    const result = u.searchParams.get("result"), q = (u.searchParams.get("q") || "").toLowerCase();
    const device = u.searchParams.get("device"), node = u.searchParams.get("node");
    return e => (!result || result.split(",").includes(e.result)) && (!device || e.client === device) && (!node || e.node === node) &&
      (!q || e.qname.includes(q) || (e.client || "").includes(q) || (names[e.client] || "").includes(q));
  };
  await page.route(/\/api\/querylog\?/, r => {
    const u = new URL(r.request().url());
    state.asked.push(u.search);
    const after = Number(u.searchParams.get("after") || 0);
    if (after && state.more.length) state.entries.push(...state.more.splice(0));
    const all = state.entries.filter(e => e.id > after).filter(pick(u));
    const shown = [...all].reverse();
    const nm = {};
    for (const e of shown) if (names[e.client]) nm[e.client] = names[e.client];
    return json(r, { entries: shown, run: "r1", last: state.entries.length, matched: all.length, held: state.entries.length, cap: 20000,
      oldest: state.entries[0].at, newest: state.entries.at(-1).at, nodes: state.nodes, names: nm });
  });
  await page.route(/\/api\/querylog\/top/, r => json(r, { entries: 7, queried: [{ name: "www.example.com", count: 2 }, { name: "ads.example.net", count: 1 }],
    blocked: [{ name: "ads.example.net", count: 1 }], clients: [{ name: LAPTOP, count: 3 }, { name: TABLET, count: 2 }], blocks: 2,
    names: { [LAPTOP]: "laptop.home.example" } }));
  // The last 40 minutes read (the controller started 40 minutes ago): a gentle wave, the
  // given values at the end.
  const series = (vs, base) => Array(20).fill(null)
    .concat(Array.from({ length: 40 - vs.length }, (_, i) => +(base * (1 + 0.3 * Math.sin(i / 3))).toFixed(3)), vs);
  await page.route(/\/api\/dashboard$/, r => json(r, { now: new Date().toISOString(), window_s: 3600, bin_s: 60, bins: 60, nodes: [], fleet: {
    nodes: 3, reading: 2, now: { qps: 0.4 }, hour: { qps: 0.4, servfail: 1, dropped: 0, blocked_share: 0.009, p50_ms: 7 },
    qps: series([0.2, 0.3, 0.2, 0.4, 0.3, 0.5], 0.35), p95_ms: series([7, 8, 7, 9, 22, 8], 8), blocked_qps: series([0.01, 0, 0.01, 0.02, 0, 0.01], 0.01),
    servfail: series([0, 0, 1, 0, 0, 0], 0) } }));
  await page.route(/\/api\/changes$/, r => {
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON();
      state.posts.push(b);
      state.changes.push({ id: `c${state.changes.length + 1}`, kind: b.kind, name: b.name, summary: b.summary,
        effect: "Blocking: compiled again, then sent to the nodes, live (no restart)" });
      return json(r, state.changes.at(-1), 201);
    }
    return json(r, { changes: state.changes, restarts: [], apply: null });
  });
  await page.route(/\/api\/changes\/file\?/, r => json(r, { kind: "source", name: "overrides-allow.txt", hash: "h1", exists: true, pending: [], text: state.allowText }));
  return state;
}

const rows = page => page.locator("#qlrows .r:not(.headr)");

test("Query log: the rows say who asked what, and why it was answered so", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  const r = await page.goto("querylog.html");
  expect(r.headers()["content-security-policy"]).toContain("script-src 'self'");
  await expect(page.locator("h1")).toHaveText("Query log");
  const nav = page.locator('nav[aria-label="Main"] a');
  await expect(nav).toHaveText(["Nodes", "Zones", "Blocking", "Query log"]);
  await expect(nav.nth(3)).toHaveAttribute("aria-current", "page");

  await expect(rows(page)).toHaveCount(7);
  const newest = rows(page).first();
  await expect(newest.locator(".site")).toHaveText("nothere.example.com");
  await expect(newest).toContainText("No such name");
  await expect(newest).toContainText("dns3");
  // A device named from the zones' records: its first label, the address under it.
  const cached = rows(page).nth(1);
  await expect(cached.locator(".dev")).toHaveText("laptop");
  await expect(cached).toContainText(LAPTOP);
  await expect(cached.locator(".ans")).toHaveText("Cache");
  await expect(rows(page).nth(2).locator(".ans")).toContainText("Failed");
  const always = rows(page).nth(3);
  await expect(always.getByText("Not kept")).toBeVisible();
  await expect(always.locator(".ans")).toContainText("Always blocked by you");
  await expect(rows(page).nth(4).locator(".ans")).toHaveText("Your zone");
  const blocked = rows(page).nth(5);
  await expect(blocked.locator(".state.bad")).toHaveText("Blocked");
  await expect(blocked.getByRole("button", { name: "Allow" })).toBeVisible();
  await expect(rows(page).nth(6).locator(".took")).toHaveText("9.1 ms");

  // What each node keeps, and what's wrong with a node's log.
  await expect(page.locator("#qlkeep")).toHaveText("dns2 keeps each device's address · dns3 keeps each device's network only (/24). Set in each node's settings.");
  await expect(page.locator("#qlkeep a").first()).toHaveAttribute("href", `node.html#${encodeURIComponent(K2)}`);
  await expect(page.locator("#qlwarn")).toContainText("dns4 keeps no query log: its firmware is from before it.");
  await expect(page.locator("#qlwarn")).toContainText("dns3 answered 12 queries faster than the controller read them");
  await expect(page.locator("#qllive")).toHaveText("Live · the last 7 queries");

  // The sites and devices asked most, and the last hour.
  await expect(page.locator("#topsites")).toContainText("www.example.com");
  await expect(page.locator("#topdevices")).toContainText("laptop");
  await expect(page.locator("#topdevices")).toContainText("Named from the records in your zones.");
  await expect(page.locator("#minis .mini")).toHaveCount(4);
  await expect(page.locator("#minis .spark")).toHaveCount(4);
  await expect(page.locator("#hourwhat")).toHaveText("2 of 3 nodes send their figures");
  const spark = page.locator("#minis .spark").first();
  const box = await spark.boundingBox();
  await spark.hover({ position: { x: box.width - 2, y: box.height / 2 } });
  await expect(page.locator("#minis .readout").first()).toContainText("a second");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Query log: the chips, the box and a device narrow the rows, kept in the address", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("querylog.html");
  await expect(rows(page)).toHaveCount(7);
  await expect(page.getByRole("button", { name: "Everything" })).toHaveAttribute("aria-pressed", "true");

  await page.getByRole("button", { name: "Blocked", exact: true }).click();
  await expect(rows(page)).toHaveCount(2);
  expect(state.asked.at(-1)).toContain("result=blocked%2Coverridden");
  await expect(page).toHaveURL(/\?show=blocked$/);

  await page.getByRole("button", { name: "Failed", exact: true }).click();
  await expect(rows(page)).toHaveCount(1);
  expect(state.asked.at(-1)).toContain("result=refused%2Cservfail%2Cerror%2Cdropped");

  await page.getByRole("button", { name: "Everything" }).click();
  await page.locator("#qlq").fill("laptop");
  await expect(rows(page)).toHaveCount(3);
  expect(state.asked.at(-1)).toContain("q=laptop");
  await expect(page).toHaveURL(/\?q=laptop$/);
  await page.locator("#qlq").fill("");
  await expect(rows(page)).toHaveCount(7);

  // A device from a row: its chip, and every device again from the chip.
  await rows(page).nth(1).locator(".dev").click();
  await expect(rows(page)).toHaveCount(3);
  expect(state.asked.at(-1)).toContain(`device=${LAPTOP}`);
  const chip = page.locator("#qlchips .chip");
  await expect(chip).toContainText("Device: laptop");
  await chip.click();
  await expect(chip).toHaveCount(0);
  await expect(rows(page)).toHaveCount(7);

  // One node, from the node chip.
  await page.locator("#qlnode").selectOption({ label: "dns3" });
  await expect(rows(page)).toHaveCount(3);
  expect(state.asked.at(-1)).toContain(`node=${encodeURIComponent(K3)}`);

  // Nothing matches: says so, and shows everything again.
  await page.locator("#qlq").fill("nothing-like-it");
  await expect(page.locator("#qlrows .empty")).toContainText("No queries match.");
  await page.getByRole("button", { name: "Show everything" }).click();
  await expect(rows(page)).toHaveCount(7);
  await expect(page.locator("#qlnode")).toHaveValue("");
  expect(await violations()).toEqual([]);
});

test("Query log: a search result's link opens the device; no failures says so", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await page.goto(`querylog.html?client=${TABLET}`);
  await expect(page.locator("#qlchips .chip")).toContainText(`Device: ${TABLET}`);
  await expect(rows(page)).toHaveCount(2);
  expect(state.asked[0]).toContain(`device=${TABLET}`);

  state.entries = state.entries.filter(e => e.result !== "servfail");
  await page.goto("querylog.html#query-log?show=failed");
  await expect(page.locator("#qlrows .empty")).toHaveText("No failed queries.");
});

test("Query log: new queries come in live; Pause holds them", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await page.goto("querylog.html");
  await expect(rows(page)).toHaveCount(7);
  state.more.push(entry(K2, LAPTOP, "new.example.com", "forwarded"));
  await expect(rows(page).first().locator(".site")).toHaveText("new.example.com", { timeout: 8000 });
  expect(state.asked.some(q => /after=7&run=r1/.test(q))).toBeTruthy();

  await page.getByRole("button", { name: "Pause" }).click();
  await expect(page.locator("#qllive")).toHaveText("Paused");
  const n = state.asked.length;
  state.more.push(entry(K2, LAPTOP, "later.example.com", "forwarded"));
  await page.waitForTimeout(4000);
  expect(state.asked.length).toBe(n);
  await expect(rows(page).first().locator(".site")).toHaveText("new.example.com");
  await page.getByRole("button", { name: "Resume" }).click();
  await expect(rows(page).first().locator(".site")).toHaveText("later.example.com");
});

test("Query log: Allow on a blocked row waits in the changes to apply", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("querylog.html");
  await rows(page).nth(5).getByRole("button", { name: "Allow" }).click();
  await expect(page.locator("#toast")).toContainText("Always allow ads.example.net: waiting to apply");
  expect(state.posts).toEqual([{ kind: "source", name: "overrides-allow.txt", text: "kept.example.com\nads.example.net\n", hash: "h1",
    summary: "Always allow ads.example.net" }]);
  await expect(page.locator(".changes .chip-btn")).toHaveText("1 change to apply");
  await page.locator(".changes .chip-btn").click();
  await expect(page.getByRole("dialog")).toContainText("Always allow ads.example.net");
  await page.keyboard.press("Escape");

  // Already allowed: nothing is added.
  state.allowText = "kept.example.com\nads.example.net\n";
  await rows(page).nth(5).getByRole("button", { name: "Allow" }).click();
  await expect(page.locator("#toast")).toContainText("ads.example.net is already always allowed");
  expect(state.posts).toHaveLength(1);
  expect(await violations()).toEqual([]);
});

test("Query log: Allow goes into the controller's own pending changes", async ({ page }) => {
  // The changes APIs are the controller's, not faked: the allow file as Apply would send it.
  await login(page);
  await fake(page);
  await page.unroute(/\/api\/changes$/);
  await page.unroute(/\/api\/changes\/file\?/);
  await page.goto("querylog.html");
  await rows(page).nth(5).getByRole("button", { name: "Allow" }).click();
  await expect(page.locator("#toast")).toContainText("Always allow ads.example.net: waiting to apply");
  const f = await apiGet(page, "api/changes/file?kind=source&name=overrides-allow.txt");
  expect(f.status).toBe(200);
  expect(f.json.text.split("\n")).toContain("ads.example.net");
  const c = await apiGet(page, "api/changes");
  const mine = c.json.changes.filter(x => x.name === "overrides-allow.txt");
  expect(mine).toHaveLength(1);
  expect(mine[0].summary).toBe("Always allow ads.example.net");
  // Leave the controller as it was for the other tests.
  await page.evaluate(async id => fetch(`api/changes/${id}/discard`, { method: "POST",
    headers: { "X-Session-Token": localStorage.getItem("espdns.session") || "" } }), mine[0].id);
});

test("Query log: a node keeping nothing, and a log not read, say so", async ({ page }) => {
  await login(page);
  await fake(page, { nodes: [
    { key: K2, name: "dns2", addr: DNS2, state: "ok", enabled: true, client: "hidden" },
    { key: K3, name: "dns3", addr: DNS3, state: "error", error: "timeout", enabled: false, client: "" },
    { key: K4, name: "dns4", addr: DNS4, state: "ok", enabled: false, client: "full" },
  ] });
  await page.goto("querylog.html");
  await expect(page.locator("#qlkeep")).toHaveText("dns2 keeps nothing about each device. Set in each node's settings.");
  await expect(page.locator("#qlwarn")).toContainText("dns3's query log couldn't be read: timeout.");
  await expect(page.locator("#qlwarn")).toContainText("dns4's query log is off: turn it on in its settings.");
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      const dir = path.join(shots, "querylog");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      await login(page);
      await fake(page);
      await page.goto("querylog.html");
      await expect(page.locator("#minis .spark")).toHaveCount(4);
      await expect(page.locator("#topdevices")).toContainText("laptop");
      await page.screenshot({ path: path.join(dir, `querylog-${width}-${scheme}.png`), fullPage: true });
      await page.getByRole("button", { name: "Blocked", exact: true }).click();
      await expect(rows(page)).toHaveCount(2);
      await page.screenshot({ path: path.join(dir, `blocked-${width}-${scheme}.png`) });
      await rows(page).nth(1).getByRole("button", { name: "Allow" }).click();
      await expect(page.locator(".changes .chip-btn")).toHaveText("1 change to apply");
      await page.locator(".changes .chip-btn").click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.screenshot({ path: path.join(dir, `allow-changes-${width}-${scheme}.png`) });
    });
  }
}
