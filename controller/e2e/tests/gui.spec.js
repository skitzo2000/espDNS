// The whole GUI with real clicks: the four pages (Nodes, Zones, Blocking, Query log), the
// node page, the search and the panels, against fake APIs (page.route, documentation
// addresses and names) on serve.sh's controller, which serves the pages, the board catalogue
// and the login. Every item of the top bar, every kind of row (a listed node, a found node, one
// on old firmware, a zone, a list, a query, a search result of each kind, a recent change) and
// every item of + Add node opens where it should, a page or a panel: no dead ends. No page
// links to one that isn't there, and none has a CSP violation. E2E_SHOTS=<dir> also takes
// every page and the main panels at 375 and 1366 px, light and dark, into that directory.
const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login } = require("./session");

const PAGES = fs.readdirSync(path.join(__dirname, "..", "..", "web")).filter(f => f.endsWith(".html"));

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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", FOUND = "198.51.100.37", OLD = "198.51.100.38", PRIMARY = "192.0.2.254";
const LAPTOP = "198.51.100.23";
const K2 = "aa:bb:cc:00:00:02", K3 = "aa:bb:cc:00:00:03", KF = "aa:bb:cc:00:00:37", KO = "aa:bb:cc:00:00:38";
const now = () => new Date().toISOString();
const series = vs => Array(60 - vs.length).fill(null).concat(vs);

function node(addr, id, name, extra = {}, status = {}) {
  return {
    id, addr, hostname: `${name}.local`, source: "settings", online: true, last_seen: now(), polled: now(), qps: 0.4, mac: `${id.slice(0, 15)}f${id.slice(-1)}`,
    status: { board: "p4-ip101", version: "0.0.4", uptime_s: 9660, net: { kind: "ethernet", hostname: `${name}.local` },
      health: { state: "healthy", answering: true, reasons: [] }, config: { name, source: "node" }, ...status },
    ...extra,
  };
}

const FOUNDNODE = { host: FOUND, id: KF, hostname: "espdns-000037.local", source: "mdns", online: true, read: true, board: "xiao-s3-sense",
  image: "esp32s3-octal", version: "0.0.4", net: "wifi", mac: "aa:bb:cc:00:00:3a", adopted: false, listed: false, configs: true, config: "dns4.json", next: "adopt" };
const OLDNODE = { host: OLD, id: KO, hostname: "espdns-000038.local", source: "mdns", online: true, read: true, board: "xiao-s3-sense",
  image: "esp32s3-octal", version: "0.0.0", net: "ethernet", mac: "aa:bb:cc:00:00:3b", adopted: false, listed: false, configs: false, next: "",
  install: "usb", refusals: ["its firmware takes no node configs"] };

const RESULTS = [
  { type: "node", title: "dns2", text: `p4-ip101 at ${DNS2}, healthy`, page: "nodes", key: K2, match: "name", state: "healthy" },
  { type: "node", title: "espdns-000037", text: `xiao-s3-sense at ${FOUND}, answering; found on the network, not added`, page: "nodes", key: KF, match: "name" },
  { type: "zone", title: "home.example", text: "hosted here: 4 records", page: "zones", key: "home.example", match: "name", state: "ok" },
  { type: "record", title: "nas.home.example", text: "A 192.0.2.10, in home.example", page: "zones", key: "nas.home.example", match: "within", zone: "home.example" },
  { type: "device", title: "laptop.home.example", text: `${LAPTOP}: asked 40 times`, page: "query-log", key: LAPTOP, match: "name" },
  { type: "site", title: "homeassistant.example", text: "asked twice", page: "query-log", key: "homeassistant.example", match: "name" },
  { type: "check", title: "home.example.org", text: "check whether it is blocked, and by which list", page: "blocking", key: "home.example.org", match: "typed" },
  { type: "rule", title: "homeads.example", text: "blocked, with its subdomains", page: "blocking", key: "homeads.example", match: "name" },
  { type: "list", title: "main", text: "a blocklist from 2 sources", page: "blocking", key: "main", match: "name" },
  { type: "change", title: "Always allow example.com", text: "pending", page: "changes", key: "c1", match: "summary", pending: true },
];

const INV = {
  zones: [
    { name: "corp.example", kind: "forward", forwarders: ["198.51.100.53"], configs: ["dns2.json", "dns3.json"], serving: 2, nodes: 2, state: "ok", text: "on every node" },
    { name: "home.example", kind: "hosted", file: "home.example.zone", serial: 2026100701, records: 4, serving: 2, nodes: 2, state: "ok", text: "on every node, as saved" },
    { name: "lab.example", kind: "secondary", primary: PRIMARY, serial: 7, records: 12, configs: ["dns2.json", "dns3.json"], serving: 2, nodes: 2, state: "ok", text: "on every node" },
  ],
  counts: { hosted: 1, secondary: 1, forward: 1, records: 16 }, primaries: [PRIMARY], nodes: 2, fleet: 2,
  primary: { set: true, address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["lab.example"] },
};
const HOME = "$TTL 3600\n@ IN SOA ns1.home.example. hostmaster.home.example. ( 2026100701 3600 600 604800 300 )\n  IN NS ns1\nns1 IN A 192.0.2.53\nnas IN A 192.0.2.10\n";

const LISTS = [
  { source: "wildcard:https://lists.example.com/pro.txt", shown: "wildcard:https://lists.example.com/pro.txt", title: "Pro", what: "ads and tracking",
    on: true, allow: false, waiting: false, domains: 227572, counted: true },
  { source: "hosts:https://lists.example.com/hosts.txt", shown: "hosts:https://lists.example.com/hosts.txt", title: "Hosts", what: "",
    on: false, allow: false, waiting: false, domains: 0, counted: false },
];

let qid = 0;
const entry = (node, client, qname, result, extra = {}) => ({ id: ++qid, node, at: new Date(Date.now() - (60 - qid) * 1000).toISOString(), client, qname, qtype: "A",
  result, rcode: "NOERROR", latency_us: 2100, ...extra });

// fake answers every API the pages read; serve.sh's controller answers the rest (the board
// catalogue and chip images for Install over USB, the session).
async function fake(page) {
  qid = 0;
  const state = {
    changes: [{ id: "c1", kind: "source", summary: "Always allow example.com", effect: "Blocking: compiled again, then sent to the nodes, live (no restart)" }],
    entries: [entry(K2, LAPTOP, "www.example.com", "forwarded"), entry(K3, LAPTOP, "ads.example.net", "blocked", { rule: "list" }),
      entry(K2, LAPTOP, "nas.home.example", "hosted")],
  };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\//, route => {
    const req = route.request(), u = new URL(req.url()), p = u.pathname.replace(/^\/api\//, ""), m = req.method();
    const q = k => u.searchParams.get(k);
    switch (`${m} ${p}`) {
      case "GET nodes": return json(route, [
        node(DNS2, K2, "dns2"),
        node(DNS3, K3, "dns3", {}, { board: "ws-s3-eth", health: { state: "degraded", answering: true, reasons: ["zone refresh failing"] } }),
        node(FOUND, KF, "espdns-000037", { source: "mdns" }, { board: "xiao-s3-sense", net: { kind: "wifi" }, config: { source: "board" } }),
      ]);
      case "GET nodes/found": return json(route, { nodes: [FOUNDNODE, OLDNODE] });
      case "POST nodes/lookup": return json(route, { address: req.postDataJSON().address, found: false, answered: false });
      case "GET adopt": return json(route, { nodes: [], configs: [{ name: "dns4.json", config_name: "dns4", address: "198.51.100.40/24", gateway: "198.51.100.1" }],
        primary: {}, key: true, no_dhcp: [], dry_run_valid_s: 900 });
      case "GET updates": return json(route, { scheme: "semver", builds: [{ source: "images/esp32s3-octal", image: "esp32s3-octal", version: "0.0.4", build: "b4" }],
        available: [DNS2], nodes: [
          { host: DNS2, listed: true, online: true, runs: { version: "0.0.1", build: "a1" }, newest: { version: "0.0.4", build: "b4", source: "builds/p4-ip101" }, state: "available", why: "" },
          { host: DNS3, listed: true, online: true, runs: { version: "0.0.4", build: "b4" }, newest: { version: "0.0.4", build: "b4" }, state: "current", why: "" }] });
      case "GET dashboard": return json(route, { now: now(), window_s: 3600, bin_s: 60, bins: 60, nodes: [], fleet: {
        nodes: 2, reading: 2, now: { qps: 0.4 }, hour: { qps: 0.4, servfail: 0, dropped: 0, blocked: 212, blocked_share: 0.009, p50_ms: 7 },
        qps: series([0.2, 0.3, 0.2, 0.4, 0.3, 0.5]), p95_ms: series([7, 8, 7, 9, 22, 8]), blocked_qps: series([0.01, 0, 0.01, 0.02, 0, 0.01]), servfail: series([0, 0, 0, 0, 0, 0]) } });
      case "GET changes": return json(route, { changes: state.changes, restarts: [], apply: null });
      case "GET key": return json(route, { source: "file", present: true, fingerprint: "0123456789abcdef", recovery: { present: true, fingerprint: "fedcba9876543210" }, nodes_listed: true });
      case "GET backup": return json(route, { last: "", parts: [{ name: "configs", files: 2, bytes: 900, included: true }, { name: "firmware", files: 4, bytes: 3 << 20, included: false }] });
      case "GET actions": return json(route, [
        { time: now(), source: "web", who: "admin", action: "job apply", event: "end", result: "done", args: ["{}"] },
        { time: now(), source: "web", who: "admin", action: "job reboot", event: "end", result: "done", args: [JSON.stringify({ node: DNS2 })] },
        { time: now(), source: "web", who: "admin", action: "zone save", event: "end", result: "done", args: ["home.example"] }]);
      case "GET settings": return json(route, { settings: { nodes: [DNS2, DNS3], primary: { address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["lab.example"] } }, version: "v1", exists: true,
        primary: { set: true, address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["lab.example"], uncovered: [] } });
      case "GET zone-inventory": return json(route, INV);
      case "GET zone-records": return json(route, { name: q("name"), primary: PRIMARY, serial: 7, more: 0, records: [] });
      case "GET zones/home.example.zone": return json(route, { name: "home.example.zone", text: HOME, hash: "h1", history: [] });
      case "GET changes/file": return json(route, { kind: q("kind"), name: q("name"), exists: true, pending: [], text: q("kind") === "zone" ? HOME : "", hash: "h1" });
      case "GET blocking/setup": return json(route, { catalog: [], groups: [], list: "main", defined: true, hash: "h", others: [], pending: false,
        report: { time: new Date(Date.now() - 6 * 3600e3).toISOString(), blocked: 227572, allowed: 0, bytes: 900000 },
        lists: LISTS, allowed: [{ site: "example.com", waiting: true }], blocked: [{ site: "homeads.example", waiting: false }],
        nodes: [{ host: DNS2, online: true, state: "on", current: true, entries: 227572, room: 3900000 }, { host: DNS3, online: true, state: "on", current: true, entries: 227572, room: 900000 }] });
      case "GET blocking/check": return json(route, { check: { name: q("name"), result: "not blocked", by: "", entries: [], specs: [], list: "main", indexed: true }, sources: [], nodes: [] });
      case "GET blocking": return json(route, { sources: [{ name: "overrides.txt" }], lists: [{ name: "main", command: "espdns blocklist -list wildcard:https://lists.example.com/pro.txt" }] });
      case "GET querylog": return json(route, { entries: [...state.entries].reverse(), run: "r1", last: state.entries.length, matched: state.entries.length, held: state.entries.length,
        cap: 20000, oldest: state.entries[0].at, newest: state.entries.at(-1).at, names: { [LAPTOP]: "laptop.home.example" },
        nodes: [{ key: K2, name: "dns2", addr: DNS2, state: "ok", enabled: true, client: "full", lost: 0 }, { key: K3, name: "dns3", addr: DNS3, state: "ok", enabled: true, client: "full", lost: 0 }] });
      case "GET querylog/top": return json(route, { entries: 3, queried: [{ name: "www.example.com", count: 1 }], blocked: [{ name: "ads.example.net", count: 1 }],
        clients: [{ name: LAPTOP, count: 3 }], blocks: 1, names: { [LAPTOP]: "laptop.home.example" } });
      case "GET search": return json(route, { query: q("q"), results: RESULTS, matched: {}, more: false, partial: false, searched: [], problems: [] });
    }
    if (m === "GET" && /^nodes\/[^/]+\/settings$/.test(p)) return json(route, { error: "not in this test" }, 404);
    return route.fallback(); // the controller's own: the session, the boards and chip images
  });
  return state;
}

// clickRow clicks a row where it holds no button or link (its small text: a board, where a
// zone comes from, a query's node), so the row itself acts, as a real click there does.
async function clickRow(page, row) {
  const at = row.locator(".sub").first();
  await expect(at).toBeVisible();
  expect(await at.evaluate(e => !e.closest("a, button"))).toBe(true);
  await at.click();
}

const drawer = page => page.locator("aside.drawer");

// links lists every link on the page to one of the controller's pages, by its file.
async function pageLinks(page) {
  return page.evaluate(() => [...document.querySelectorAll("a[href]")]
    .map(a => new URL(a.getAttribute("href"), location.href))
    .filter(u => u.origin === location.origin)
    .map(u => u.pathname.replace(/^\//, "") || "index.html"));
}

const NAV = [["Nodes", /\/$/], ["Zones", /\/zones\.html$/], ["Blocking", /\/blocklists\.html$/], ["Query log", /\/querylog\.html/]];

test("the top bar: every item goes to its page from every page; nothing links to a page that isn't there", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  for (const start of ["./", "zones.html", "blocklists.html", "querylog.html", `node.html#${K2}`]) {
    for (const [name, url] of NAV) {
      await page.goto(start);
      await page.locator("nav.main").getByRole("link", { name, exact: true }).click();
      await expect(page).toHaveURL(url);
      await expect(page.locator("nav.main").getByRole("link", { name, exact: true })).toHaveAttribute("aria-current", "page");
      await page.waitForLoadState("networkidle");
      for (const l of await pageLinks(page)) expect(PAGES, `${name} from ${start} links to ${l}`).toContain(l);
    }
  }
  // The brand goes home; the account chip and the changes chip open their panels.
  await page.goto("zones.html");
  await page.locator(".top a.brand").click();
  await expect(page).toHaveURL(/\/$/);
  await page.getByRole("button", { name: /^Account: / }).click();
  await expect(drawer(page)).toContainText("Keep the controller safe");
  await page.keyboard.press("Escape");
  await page.locator(".changes .chip-btn").click();
  await expect(drawer(page)).toContainText("Changes to apply");
  await expect(drawer(page)).toContainText("Always allow example.com");
  expect(await violations()).toEqual([]);
});

test("Nodes: a listed node's row opens its page; a found one its setup; old firmware Install over USB", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  const rows = page.locator("#nodes .r:not(.headr)");
  await expect(rows).toHaveCount(4); // dns2, dns3, the found node, the old one
  // A listed node: its page.
  await clickRow(page, rows.nth(1));
  await expect(page).toHaveURL(new RegExp(`/node\\.html#${encodeURIComponent(K3)}$`));
  await expect(page.locator("#name")).toHaveText("dns3");
  await expect(page.locator("#attention")).toContainText("Zone refresh failing");
  // Back: the found node ready to add: its setup panel, then the Add flow.
  await page.locator("nav.main").getByRole("link", { name: "Nodes" }).click();
  await clickRow(page, rows.nth(2));
  await expect(drawer(page)).toContainText("Add it to your nodes");
  await drawer(page).getByRole("button", { name: "Add node" }).click();
  await expect(page.locator("#nnname")).toHaveValue("dns4.json");
  // A new name is offered too: no Configs page needed.
  await expect(page.locator("#nnname option", { hasText: "A new name…" })).toHaveCount(1);
  await page.locator("#nnname").selectOption("+new");
  await expect(page.locator("#nnnew")).toBeVisible();
  await page.locator("#nq").press("Escape");
  await page.mouse.click(5, 300);
  // The old firmware one: its setup says why, Update over USB opens Install over USB on its board.
  await clickRow(page, rows.nth(3));
  await expect(drawer(page)).toContainText("Update its firmware over USB");
  await drawer(page).getByRole("button", { name: "Update over USB" }).click();
  const usb = page.getByRole("dialog", { name: /^Update .* over USB$/ });
  await expect(usb).toBeVisible();
  await expect(usb.locator("#usb-board")).toHaveValue("xiao-s3-sense");
  await expect(usb.locator("#usb-addr")).toHaveValue(`${OLD}/24`);
  // serve.sh's controller has no chip image imported: Prepare waits, saying how to load one.
  await expect(usb.locator("#usb-prepare")).toBeDisabled();
  await expect(usb.locator("#usb-facts")).toContainText("isn't in the controller yet");
  // Describe your own board: the custom form, in the panel.
  await usb.locator("#usb-board").selectOption("+");
  await expect(usb.locator("#usb-form")).toBeVisible();
  await expect(usb.locator("#usb-save")).toBeVisible();
  expect(await violations()).toEqual([]);
});

test("+ Add node: every item opens its next step", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  const add = page.getByRole("button", { name: "+ Add node" });
  const items = page.locator("#nqres .opt.pick");
  const clickAt = async loc => { const b = await loc.boundingBox(); await page.mouse.click(b.x + 12, b.y + b.height / 2); };
  await add.click();
  await expect(items).toHaveCount(3);
  const n = await items.count();
  for (let i = 0; i < n; i++) {
    if (i) await add.click();
    const text = await items.nth(i).innerText();
    await clickAt(items.nth(i));
    await expect(drawer(page), `item ${i}: ${text}`).toBeVisible();
    await expect(drawer(page).locator("h2")).toHaveText(i === 2 ? "Install a board over USB" : "Seeed XIAO ESP32S3 Sense");
    await page.keyboard.press("Escape");
    await expect(drawer(page)).toHaveCount(0);
  }
  // An address with no node: Install a board over USB.
  await add.click();
  await page.locator("#nq").fill("192.0.2.99");
  await page.locator("#nq").press("Enter");
  await page.locator("#nqres").getByRole("button", { name: "Install a board over USB" }).click();
  await expect(drawer(page).locator("h2")).toHaveText("Install a board over USB");
  expect(await violations()).toEqual([]);
});

test("the node page: a found node is set up from it; an update is a click away", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto(`node.html#${KF}`);
  await expect(page.locator("#attention")).toContainText("isn't one of your nodes yet");
  await page.getByRole("link", { name: "Set it up" }).click();
  await expect(page).toHaveURL(/\/(index\.html)?$/);
  await expect(drawer(page)).toContainText("Add it to your nodes");
  // dns2's update: Update firmware on its page.
  await page.goto(`node.html#${K2}`);
  await page.getByRole("button", { name: "Update firmware" }).click();
  await expect(drawer(page).locator("h2")).toHaveText("Update firmware");
  expect(await violations()).toEqual([]);
});

test("Zones, Blocking and Query log: every row opens its place", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  // A zone row: its page.
  await page.goto("zones.html");
  const z = page.locator(".zones .r[data-href]");
  await expect(z).toHaveCount(3);
  for (const name of ["corp.example", "home.example", "lab.example"]) {
    await page.goto("zones.html");
    await clickRow(page, page.locator(`.zones .r[data-zone="${name}"]`));
    await expect(page).toHaveURL(new RegExp(`#zone=${name.replace(".", "\\.")}$`));
    await expect(page.locator("main")).toContainText(name);
    await expect(page.getByRole("link", { name: "‹ Zones" })).toBeVisible();
  }
  // A list row and a rule row: they open.
  await page.goto("blocklists.html");
  for (const title of ["Pro", "Hosts"]) {
    const d = page.locator("#lists details.ex", { hasText: title });
    await d.locator("summary").click();
    await expect(d).toHaveAttribute("open", "");
  }
  const allowRow = page.locator("#rules details.ex").first();
  await expect(allowRow).toHaveAttribute("open", "");
  // A query row: its panel, then on to Blocking and to its node.
  await page.goto("querylog.html");
  const q = page.locator("#qlrows .r[data-q]");
  await expect(q).toHaveCount(3);
  await clickRow(page, q.nth(1)); // ads.example.net, blocked
  await expect(drawer(page).locator("h2")).toHaveText("ads.example.net");
  await drawer(page).getByRole("link", { name: "Check it on Blocking" }).click();
  await expect(page).toHaveURL(/\/blocklists\.html#check=ads\.example\.net$/);
  await expect(page.locator("#checkq")).toHaveValue("ads.example.net");
  await page.goto("querylog.html");
  await clickRow(page, q.nth(0));
  await drawer(page).getByRole("button", { name: "Only this site" }).click();
  await expect(page).toHaveURL(/\?q=nas\.home\.example/);
  await clickRow(page, q.nth(0));
  await drawer(page).getByRole("link", { name: "Open the node" }).click();
  await expect(page).toHaveURL(new RegExp(`/node\\.html#${encodeURIComponent(K2)}$`));
  await expect(page.locator("#name")).toHaveText("dns2");
  expect(await violations()).toEqual([]);
});

test("the search: every kind of result opens its place", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  const box = page.locator("#q");
  const expectations = [
    [0, new RegExp(`/node\\.html#${encodeURIComponent(K2)}$`), async () => expect(page.locator("#name")).toHaveText("dns2")],
    [1, new RegExp(`/node\\.html#${encodeURIComponent(KF)}$`), async () => expect(page.getByRole("link", { name: "Set it up" })).toBeVisible()],
    [2, /\/zones\.html#zone=home\.example$/, async () => expect(page.getByRole("link", { name: "‹ Zones" })).toBeVisible()],
    [3, /\/zones\.html#zone=home\.example$/, async () => expect(page.getByRole("link", { name: "‹ Zones" })).toBeVisible()],
    [4, new RegExp(`/querylog\\.html\\?(client|device)=${LAPTOP.replace(/\./g, "\\.")}$`), async () => expect(page.getByRole("button", { name: /^Device .*: show every device$/ })).toBeVisible()],
    [5, /\/querylog\.html\?(name|q)=homeassistant\.example$/, async () => expect(page.locator("#qlq")).toHaveValue("homeassistant.example")],
    [6, /\/blocklists\.html#check=home\.example\.org$/, async () => expect(page.locator("#checkq")).toHaveValue("home.example.org")],
    [7, /\/blocklists\.html#check=homeads\.example$/, async () => expect(page.locator("#checkq")).toHaveValue("homeads.example")],
    [8, /\/blocklists\.html#list=main$/, async () => expect(page.locator("#lists details.ex")).toHaveCount(2)],
  ];
  for (const [i, url, then] of expectations) {
    await page.goto("./");
    await box.fill("home");
    await expect(page.locator("#q-res [role=option]")).toHaveCount(RESULTS.length);
    await page.locator("#q-res [role=option]").nth(i).click();
    await expect(page, RESULTS[i].title).toHaveURL(url);
    await then();
  }
  // A change: the changes panel.
  await page.goto("./");
  await box.fill("home");
  await page.locator("#q-res [role=option]").nth(9).click();
  await expect(drawer(page)).toContainText("Changes to apply");
  expect(await violations()).toEqual([]);
});

test("the account panel: a recent change about a node or a zone opens it", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  await page.getByRole("button", { name: /^Account: / }).click();
  await drawer(page).locator("summary", { hasText: "Recent changes" }).click();
  await drawer(page).getByRole("link", { name: "Zone home.example saved" }).click();
  await expect(page).toHaveURL(/\/zones\.html#zone=home\.example$/);
  await page.getByRole("button", { name: /^Account: / }).click();
  await drawer(page).locator("summary", { hasText: "Recent changes" }).click();
  await drawer(page).getByRole("link", { name: /^Restarted on / }).click();
  await expect(page).toHaveURL(/\/node\.html#192\.0\.2\.53$/);
  await expect(page.locator("#name")).toHaveText("dns2");
  // The backup's options are here: no Backup page.
  await page.getByRole("button", { name: /^Account: / }).click();
  await drawer(page).locator("summary", { hasText: "Keep the controller safe" }).click();
  await drawer(page).locator("#bp-more summary").click();
  await expect(drawer(page).locator("#bp-age")).toBeVisible();
  await expect(drawer(page).locator("#bp-fw")).toBeVisible();
  await expect(drawer(page).locator("#bp-parts")).toContainText("firmware");
  expect(await violations()).toEqual([]);
});

// The screenshots for review (E2E_SHOTS=<dir>): every page and the main panels; not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: every page and panel, ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      test.setTimeout(120000);
      fs.mkdirSync(shots, { recursive: true });
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 900 });
      await login(page);
      await fake(page);
      const shot = async (name, full = true) => {
        await page.waitForLoadState("networkidle");
        await page.screenshot({ path: path.join(shots, `${name}-${width}-${scheme}.png`), fullPage: full });
      };
      const close = async () => { await page.keyboard.press("Escape"); await expect(drawer(page)).toHaveCount(0); };
      await page.goto("./");
      await expect(page.locator("#nodes .r:not(.headr)")).toHaveCount(4);
      await shot("nodes");
      await page.getByRole("button", { name: "+ Add node" }).click();
      await shot("add-node");
      await page.locator("#nq").press("Escape");
      await page.mouse.click(2, 400);
      await clickRow(page, page.locator("#nodes .r.r-new").nth(1));
      await shot("found-setup", false);
      await drawer(page).getByRole("button", { name: "Update over USB" }).click();
      await expect(page.locator("#usb-board")).toHaveValue("xiao-s3-sense");
      await shot("install-over-usb", false);
      await page.locator("#usb-board").selectOption("+");
      await shot("install-over-usb-custom", false);
      await close();
      await page.locator(".changes .chip-btn").click();
      await shot("changes", false);
      await close();
      await page.getByRole("button", { name: /^Account: / }).click();
      await drawer(page).locator("summary", { hasText: "Keep the controller safe" }).click();
      await shot("account", false);
      await close();
      await page.goto(`node.html#${K3}`);
      await expect(page.locator("#name")).toHaveText("dns3");
      await shot("node");
      await page.goto(`node.html#${KF}`);
      await expect(page.getByRole("link", { name: "Set it up" })).toBeVisible();
      await shot("node-found");
      await page.goto("zones.html");
      await expect(page.locator(".zones .r[data-href]")).toHaveCount(3);
      await shot("zones");
      await page.goto("zones.html#zone=home.example");
      await expect(page.getByRole("link", { name: "‹ Zones" })).toBeVisible();
      await expect(page.locator("table.recs")).toBeVisible(); // its records read, not "Reading its records…"
      await shot("zone");
      await page.goto("blocklists.html");
      await expect(page.locator("#lists details.ex")).toHaveCount(2);
      await shot("blocking");
      await page.goto("querylog.html");
      await expect(page.locator("#qlrows .r[data-q]")).toHaveCount(3);
      await shot("querylog");
      await clickRow(page, page.locator("#qlrows .r[data-q]").nth(1));
      await shot("query", false);
      await close();
      if (!await page.locator("#q").isVisible()) await page.locator(".search-btn").click(); // a phone: the icon opens it
      await page.locator("#q").fill("home");
      await expect(page.locator("#q-res [role=option]")).toHaveCount(RESULTS.length);
      await shot("search", false);
    });
  }
}
