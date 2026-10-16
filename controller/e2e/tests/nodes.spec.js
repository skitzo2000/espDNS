// The Nodes page (index.html, home.js, addnode.js) and the shell it shares (shell.js),
// against a fake controller: serve.sh's controller logs in and serves the pages, and the
// APIs the page reads are answered here (page.route), with documentation addresses
// (RFC 5737), so nothing goes on the network. E2E_SHOTS=<dir> also takes the page and the
// + Add node panel at 375 and 1366 px, light and dark, into that directory.
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login } = require("./session");

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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", FOUND = "198.51.100.37", TYPED = "192.0.2.77", NOBODY = "192.0.2.99";
// The interfaces' MACs (/api/nodes "mac", /api/nodes/found "mac"): dns3 runs on DHCP.
const MAC2 = "aa:bb:cc:00:00:05", MAC3 = "aa:bb:cc:00:00:06", MACFOUND = "aa:bb:cc:00:00:3a", MACTYPED = "aa:bb:cc:00:00:7a";

function node(addr, id, name, board, health, extra = {}) {
  return {
    id, addr, hostname: `espdns-${id.slice(-5).replace(":", "")}.local`, source: "settings", online: true,
    last_seen: new Date().toISOString(), polled: new Date().toISOString(), qps: 0.4,
    status: { board, version: "0.0.1", uptime_s: 9660, net: { kind: "ethernet", hostname: `${name}.local` }, health, config: { name, source: "node" } },
    ...extra,
  };
}

// dhcp is n on an address from DHCP (its /status config.address).
function dhcp(n) { n.status.config.address = "dhcp"; return n; }

const series = vs => Array(60 - vs.length).fill(null).concat(vs); // the newest minutes

// fake answers the page's APIs; state.changes is what /api/changes lists, state.posts what
// was sent.
async function fake(page) {
  const state = {
    changes: [{ id: "c1", kind: "lists", summary: "Always allow example.com", effect: "Blocking: compiled again, then sent to the nodes, live (no restart)" }],
    posts: [],
  };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/nodes$/, r => json(r, [
    node(DNS2, "aa:bb:cc:00:00:02", "dns2", "p4-ip101", { state: "healthy", answering: true, reasons: [] }, { mac: MAC2 }),
    dhcp(node(DNS3, "aa:bb:cc:00:00:03", "dns3", "ws-s3-eth", { state: "degraded", answering: true, reasons: ["zone refresh failing"] }, { qps: 0, mac: MAC3 })),
  ]));
  const found = { host: FOUND, id: "aa:bb:cc:00:00:37", hostname: "espdns-000037.local", source: "mdns", online: true, read: true,
    board: "xiao-s3-sense", version: "0.0.4", net: "wifi", mac: MACFOUND, adopted: false, listed: false, configs: true, config: "dns4.json", next: "adopt",
    address: `${FOUND}/24`, gateway: "198.51.100.1" }; // the static address it runs on (its /status config)
  await page.route(/\/api\/nodes\/found$/, r => json(r, { nodes: [found] }));
  await page.route(/\/api\/nodes\/lookup$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["lookup", b]);
    if (b.address === TYPED) return json(r, { address: TYPED, found: true, node: { ...found, host: TYPED, address: `${TYPED}/24`, gateway: "192.0.2.1", id: "aa:bb:cc:00:00:77", source: "lookup", net: "ethernet", mac: MACTYPED, board: "ws-s3-eth" } });
    return json(r, { address: b.address, found: false, answered: false, error: `${b.address}: no answer` });
  });
  await page.route(/\/api\/adopt$/, r => json(r, { nodes: [], configs: [{ name: "dns4.json", config_name: "dns4", address: "198.51.100.40/24", gateway: "198.51.100.1" },
    { name: "dns2.json", config_name: "dns2" }],
    primary: {}, key: true, no_dhcp: [], dry_run_valid_s: 900 }));
  // The config files (GET, POST and delete /api/configs/<name>): state.files by name, each
  // its JSON and hash; what is saved or deleted is in state.posts.
  state.files = { "dns4.json": { data: { name: "dns4", network: { address: "198.51.100.40/24", gateway: "198.51.100.1" } }, hash: "h1" } };
  await page.route(/\/api\/configs\/[^/]+(\/delete)?$/, r => {
    const parts = new URL(r.request().url()).pathname.split("/");
    const del = parts.at(-1) === "delete", name = decodeURIComponent(del ? parts.at(-2) : parts.at(-1));
    const f = state.files[name];
    if (r.request().method() === "GET") {
      return f ? json(r, { name, text: `${JSON.stringify(f.data, null, 2)}\n`, hash: f.hash }) : json(r, { error: `no config ${name}` }, 404);
    }
    const b = r.request().postDataJSON();
    state.posts.push([del ? "delete" : "config", name, b]);
    if ((f ? f.hash : "") !== b.hash) return json(r, { error: `${name} changed meanwhile` }, 409);
    if (del) { delete state.files[name]; return json(r, { name, deleted: true }); }
    const hash = `h${Object.keys(state.files).length + state.posts.length}`;
    state.files[name] = { data: JSON.parse(b.text), hash };
    return json(r, { name, text: b.text, hash });
  });
  await page.route(/\/api\/adopt\/preview$/, r => {
    state.posts.push(["preview", r.request().postDataJSON()]);
    return json(r, { refusal: "a test: no adoption here" });
  });
  await page.route(/\/api\/updates$/, r => json(r, { scheme: "semver", builds: [], available: [DNS2], nodes: [
    { host: DNS2, listed: true, online: true, runs: { version: "0.0.1", build: "a1" }, newest: { version: "0.0.4", build: "b4", source: "builds/p4-ip101" }, state: "available", why: "" },
    { host: DNS3, listed: true, online: true, runs: { version: "0.0.4", build: "b4" }, newest: { version: "0.0.4", build: "b4", source: "builds/ws-s3-eth" }, state: "current", why: "" },
  ] }));
  await page.route(/\/api\/dashboard$/, r => json(r, { window_s: 3600, bin_s: 60, bins: 60, nodes: [], fleet: {
    now: { qps: 0.4 }, hour: { qps: 0.4, servfail: 0, dropped: 0, blocked_share: 0.009, p50_ms: 7 },
    qps: series([0.2, 0.3, 0.2, 0.4, 0.3, 0.5, 0.4, 0.6, 0.5, 0.4]), p95_ms: series([7, 8, 7, 9, 22, 8, 7, 8, 7, 7]),
    blocked_qps: series([0.01, 0, 0.01, 0.01, 0.02, 0.01, 0, 0.01, 0.01, 0.01]) } }));
  await page.route(/\/api\/changes$/, r => json(r, { changes: state.changes, restarts: [], apply: null }));
  await page.route(/\/api\/jobs$/, r => {
    if (r.request().method() !== "POST") return r.fallback(); // a GET: the controller's own
    const b = r.request().postDataJSON();
    state.posts.push(["job", b]);
    return json(r, { id: "j1", kind: b.kind, who: "admin", state: "running", created: new Date().toISOString() }, 201);
  });
  await page.route(/\/api\/jobs\/j1\/events/, r => {
    state.changes = [];
    const progress = { changes: ["c1"], steps: [{ name: "check", state: "done" }, { name: "write", state: "done" }, { name: "compile", state: "done" },
      { name: "dry run", state: "done" }, { name: "push", state: "done" }],
    rollouts: [{ kind: "blocklist", what: "Blocklist", order: [DNS3, DNS2], state: "done", nodes: { [DNS3]: { state: "done" }, [DNS2]: { state: "done" } } }] };
    const end = { id: "j1", kind: "apply", who: "admin", state: "done", progress };
    return r.fulfill({ status: 200, contentType: "text/event-stream",
      body: `id: 1\nevent: progress\ndata: ${JSON.stringify(progress)}\n\nid: 2\nevent: end\ndata: ${JSON.stringify(end)}\n\n` });
  });
  return state;
}

test("Nodes: the list renders from the controller's nodes", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  await expect(page.locator("#sentence")).toHaveText("DNS is answering · dns3 needs attention");
  await expect(page.locator("#sentence-sub")).toContainText("Zone refresh failing");
  const nav = page.locator('nav[aria-label="Main"] a');
  await expect(nav).toHaveText(["Nodes", "Zones", "Blocking", "Query log"]);
  await expect(nav.first()).toHaveAttribute("aria-current", "page");

  const rows = page.locator("#nodes .r:not(.headr)");
  await expect(rows).toHaveCount(3);
  const dns2 = rows.nth(0), dns3 = rows.nth(1), found = rows.nth(2);
  await expect(dns2.locator(".name")).toHaveText("dns2");
  await expect(dns2.locator(".ip")).toHaveText(DNS2);
  // One line each: the board on hovering the name, an out-of-date node's dot amber with its
  // update's button beside it, a degraded one's with its reason, the link as an icon, the
  // uptime in its own column.
  await expect(dns2.locator(".name")).toHaveAttribute("title", "Guition JC-ESP32P4-M3-DEV");
  await expect(dns2).not.toContainText("Guition");
  await expect(dns2.locator(".led")).toHaveClass(/\btodo\b/);
  await expect(dns2.locator(".led")).toHaveAttribute("aria-label", "Healthy · Update available: 0.0.1 → 0.0.4");
  await expect(dns2.locator(".flag")).toHaveCount(0);
  await expect(dns2.getByRole("button", { name: "Update dns2 from 0.0.1 to 0.0.4" })).toHaveText("Update 0.0.1 → 0.0.4");
  await expect(dns2.getByRole("img", { name: "Wired", exact: true })).toBeVisible();
  await expect(dns2).toContainText("2 h 41 m");
  await expect(dns2).not.toContainText("Wired");
  await expect(dns3.locator(".upd")).toHaveCount(0);
  await expect(dns2).not.toHaveClass(/r-warn|r-bad/);
  await expect(dns3.locator(".led")).toHaveClass(/\bdegraded\b/);
  await expect(dns3).toHaveClass(/\br-warn\b/);
  await expect(dns3.locator(".led")).toHaveAttribute("title", "Degraded · Zone refresh failing");
  await expect(dns3.locator(".flag")).toHaveText("Zone refresh failing");
  await expect(found).toHaveClass(/\br-new\b/);
  await expect(found).toContainText("New device found");
  await expect(found.locator(".led")).toHaveClass(/\btodo\b/);
  await expect(found.locator(".flag")).toHaveText("Not added yet");
  await expect(found.getByRole("img", { name: "Wi-Fi", exact: true })).toBeVisible();
  await expect(found.locator(".ipc:not(.mac) .ip")).toHaveText(FOUND);
  await expect(dns2.locator(".name")).toHaveAttribute("href", `node.html#${encodeURIComponent("aa:bb:cc:00:00:02")}`);

  await expect(page.locator("#facts")).toContainText("2 of 2 nodes answering");
  await expect(page.getByRole("button", { name: "Update all" })).toBeHidden(); // one update: its row's button only
  await expect(page.locator("#minis .spark")).toHaveCount(3);
  await expect(page.locator("#copy-dns")).toHaveAttribute("title", `${DNS2}, ${DNS3}`);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// Installing and adding are two steps: Install over USB, its own button beside + Add node,
// opens the panel; + Add node has no new board in it.
test("Install over USB: its own button beside + Add node opens the panel", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  const usb = page.locator("#head-actions").getByRole("button", { name: "Install over USB" });
  await expect(usb).toHaveClass("btn");
  await expect(page.locator("#head-actions > *").last()).toHaveAttribute("id", "add-node"); // + Add node last, Install over USB just before
  await usb.click();
  const u = page.getByRole("dialog", { name: "Install a board over USB" });
  await expect(u).toBeVisible();
  await expect(u.locator("#usb-board option").nth(1)).toBeAttached();
  await expect(u.locator("#usb-addr")).toHaveValue("");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("+ Add node: the found nodes, then an address looked up", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const box = page.locator("#nq"), list = page.locator("#nqres");
  await expect(box).toBeFocused();
  await expect(list).toContainText("Found on your network");
  await expect(list).toContainText(FOUND);
  // Nodes on the network only: a new board is Install over USB's, its own button.
  await expect(list).not.toContainText("A new board over USB");
  await expect(list.locator(".opt.pick")).toHaveCount(1);

  // Nothing at this address: a new board is installed with Install over USB first.
  await box.fill(NOBODY);
  await box.press("Enter");
  await expect(list).toContainText("No espDNS node answers there. A new board is installed with Install over USB first, then added here.");
  // A node at this one: chosen, with its name to give it and the address it runs on.
  await box.fill(TYPED);
  await box.press("Enter");
  await expect(list.locator("#nnname")).toHaveValue("dns4.json");
  await expect(list.locator("#nnname option:checked")).toHaveText("dns4"); // the config's name, not its file
  // dns2's config is dns2's: offered, not to be picked.
  await expect(list.locator('#nnname option[value="dns2.json"]')).toHaveText("dns2 (in use)");
  await expect(list.locator('#nnname option[value="dns2.json"]')).toHaveAttribute("disabled", "");
  await expect(list.locator("#nnip")).toHaveValue(`${TYPED}/24`);
  await expect(list).toContainText(`Found at ${TYPED} over Wired`);
  await expect(list.getByRole("button", { name: "Add node" })).toBeEnabled();
  expect(state.posts.filter(p => p[0] === "lookup").map(p => p[1].address)).toEqual(expect.arrayContaining([NOBODY, TYPED]));

  // A listed node's address is one of the nodes already: not looked up.
  await list.getByRole("button", { name: "Back" }).click();
  const before = state.posts.length;
  await box.fill(DNS2);
  await expect(list).toContainText("Already one of your nodes");
  expect(state.posts.length).toBe(before);

  // Esc closes it.
  await box.fill("");
  await box.press("Escape");
  await expect(page.getByRole("button", { name: "+ Add node" })).toBeVisible();
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// The address box starts as the address the node runs on; choosing a name never changes it
// (not to the config's address either). What it holds, with its gateway, is saved to the
// config file first (against the version read), then sent to the adoption.
test("+ Add node: the address box is the node's, whatever name is chosen, and is what is saved and sent", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  const name = list.locator("#nnname"), ip = list.locator("#nnip"), gw = list.locator("#nngw");
  await expect(name).toHaveValue("dns4.json");
  await expect(list.locator("#nnhow-fixed")).toBeChecked(); // it runs a fixed address
  await expect(ip).toHaveValue(`${FOUND}/24`);
  await expect(gw).toHaveValue("198.51.100.1");
  await name.selectOption("");
  await expect(ip).toHaveValue(`${FOUND}/24`);
  await name.selectOption("dns4.json");
  await expect(ip).toHaveValue(`${FOUND}/24`); // not the config's 198.51.100.40/24
  const previews = () => state.posts.filter(p => p[0] === "preview").map(p => p[1]);
  const saves = () => state.posts.filter(p => p[0] === "config");
  await list.getByRole("button", { name: "Add node" }).click();
  await expect(list).toContainText("a test: no adoption here");
  expect(saves().map(p => [p[1], p[2].hash, JSON.parse(p[2].text)])).toEqual(
    [["dns4.json", "h1", { name: "dns4", network: { address: `${FOUND}/24`, gateway: "198.51.100.1" } }]]);
  expect(previews()).toEqual([{ node: FOUND, node_id: "aa:bb:cc:00:00:37", config: "dns4.json", address: `${FOUND}/24`,
    gateway: "198.51.100.1", static: true }]);

  // One typed is saved (against the version the first save made) and sent as typed.
  const saved = state.files["dns4.json"].hash;
  await list.getByRole("button", { name: "Back" }).click();
  await list.getByRole("button", { name: "Add" }).click();
  await expect(gw).toHaveValue("198.51.100.1");
  await ip.fill("198.51.100.41/24");
  await list.getByRole("button", { name: "Add node" }).click();
  await expect.poll(() => previews().length).toBe(2);
  expect(previews()[1].address).toBe("198.51.100.41/24");
  expect(JSON.parse(saves()[1][2].text).network.address).toBe("198.51.100.41/24");
  expect(saves()[1][2].hash).toBe(saved);
});

// A new name: its config written first, with its network, then the adoption sent with it and
// the node's fixed address, which choosing the name leaves alone.
test("+ Add node: a new name, with the node's fixed address", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  const ip = list.locator("#nnip"), go = list.getByRole("button", { name: "Add node" }), nn = list.locator("#nnnew");
  await list.locator("#nnname").selectOption("+new");
  await expect(nn).toBeVisible();
  await expect(list.locator("#nndel")).toHaveCount(0);
  await expect(ip).toHaveValue(`${FOUND}/24`);
  const saved = list.locator("#nnsaved");
  await expect(go).toBeDisabled(); // no name yet, and it says so
  await expect(go).toHaveAttribute("title", "Pick a name for it.");
  await expect(go).toHaveAccessibleDescription("Pick a name for it.");
  await expect(saved).toContainText("Saved to a new config file named after it.");
  await nn.fill("not a name");
  await expect(go).toBeDisabled();
  await expect(go).toHaveAttribute("title", "The new name: lowercase letters, digits and dashes.");
  await expect(saved).not.toContainText(".json");
  // Typed in capitals, it is lowercased as typed, and fine.
  await nn.fill("");
  await nn.pressSequentially("DNS3-s3");
  await expect(nn).toHaveValue("dns3-s3");
  await expect(go).toBeEnabled();
  await expect(go).toHaveAttribute("title", "");
  await expect(saved).toContainText("Saved to dns3-s3.json.");
  await nn.press("Home"); // the caret stays where it was typed
  await nn.pressSequentially("AB");
  await expect(nn).toHaveValue("abdns3-s3");
  await nn.fill("dns5");
  await expect(go).toBeEnabled();
  await ip.fill("dhcp");
  await expect(go).toBeDisabled(); // the fixed choice needs a fixed address
  await expect(go).toHaveAttribute("title", "A fixed address, with its prefix length, like 192.0.2.10/24.");
  await ip.fill(`${FOUND}/24`);
  await go.click();
  await expect(list).toContainText("a test: no adoption here");
  expect(state.posts.filter(p => p[0] === "config").map(p => [p[1], p[2].hash, JSON.parse(p[2].text)])).toEqual(
    [["dns5.json", "", { name: "dns5", network: { address: `${FOUND}/24`, gateway: "198.51.100.1" } }]]);
  expect(state.posts.filter(p => p[0] === "preview").map(p => p[1])).toEqual(
    [{ node: FOUND, node_id: "aa:bb:cc:00:00:37", config: "dns5.json", address: `${FOUND}/24`, gateway: "198.51.100.1", static: true }]);
});

// + Add node all the way: the new name's config starts with the zones to copy from the zone
// primary; the node's address is in none of the networks the primary allows, so that is
// said once, plainly, and the node is added all the same: nothing to confirm by hand.
test("+ Add node: the zone primary's zones, and what to allow there", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  const ADVICE = `Your zone primary must allow zone transfers from ${FOUND}: allow its network (e.g. 198.51.100.0/24) there once.`;
  await page.route(/\/api\/adopt$/, r => r.fulfill({ contentType: "application/json", body: JSON.stringify({ nodes: [], configs: [],
    primary: { set: true, address: "192.0.2.254", allows: ["192.0.2.0/24"], zones: ["home.example"], uncovered: [] }, key: true, no_dhcp: [], dry_run_valid_s: 900 }) }));
  await page.route(/\/api\/adopt\/preview$/, r => {
    state.posts.push(["preview", r.request().postDataJSON()]);
    return r.fulfill({ contentType: "application/json", body: JSON.stringify({ address: `${FOUND}/24`, gateway: "198.51.100.1", how: "kept",
      check: { ok: true }, primary: "192.0.2.254", zones: ["home.example"], advice: ADVICE }) });
  });
  let n = 0;
  await page.route(/\/api\/jobs$/, r => {
    if (r.request().method() !== "POST") return r.fallback();
    state.posts.push(["job", r.request().postDataJSON()]);
    n++;
    return r.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ id: `a${n}`, kind: "adopt", who: "admin", state: "running" }) });
  });
  await page.route(/\/api\/jobs\/a\d\/events/, r => {
    const id = r.request().url().match(/jobs\/(a\d)\//)[1];
    const progress = { dry_run: id === "a1", steps: [{ n: 4, title: "The zone primary", state: "done" }], primary: { primary: "192.0.2.254", zones: ["home.example"], advice: ADVICE },
      hints: [ADVICE], outcome: id === "a1" ? "dry run passed" : "adopted" };
    const end = { id, kind: "adopt", who: "admin", state: "done", progress };
    return r.fulfill({ status: 200, contentType: "text/event-stream", body: `id: 1\nevent: end\ndata: ${JSON.stringify(end)}\n\n` });
  });
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  await list.locator("#nnname").selectOption("+new");
  await list.locator("#nnnew").fill("dns5");
  await list.locator("#nnip").fill(`${FOUND}/24`);
  await list.getByRole("button", { name: "Add node" }).click();
  await expect(page.locator("#toast")).toContainText(`dns5 is added. ${ADVICE}`);
  expect(state.posts.filter(p => p[0] === "config").map(p => JSON.parse(p[2].text))).toEqual(
    [{ name: "dns5", network: { address: `${FOUND}/24`, gateway: "198.51.100.1" }, secondary: { primary: "192.0.2.254", zones: ["home.example"] } }]);
  const jobs = state.posts.filter(p => p[0] === "job").map(p => p[1].params);
  expect(jobs).toHaveLength(2);
  expect(jobs[1]).toMatchObject({ after: "a1", add_to_settings: true });
  expect(jobs[1].primary_done).toBeUndefined();
  await expect(page.getByText("I made these changes")).toHaveCount(0);
  expect(await violations()).toEqual([]);
});

// The case + Add node couldn't handle: a Wi-Fi node on DHCP on another subnet, given a
// config made for another subnet (its static address and gateway are 192.0.2.x).
async function farNode(page, state, noDHCP = []) {
  state.files["dns4.json"] = { data: { name: "dns4", network: { address: "192.0.2.253/23", gateway: "192.0.2.1" } }, hash: "h1" };
  await page.route(/\/api\/nodes\/found$/, r => r.fulfill({ contentType: "application/json", body: JSON.stringify({ nodes: [{
    host: FOUND, id: "aa:bb:cc:00:00:37", source: "mdns", online: true, read: true, board: "xiao-s3-sense", version: "0.0.4", net: "wifi",
    mac: MACFOUND, adopted: false, listed: false, configs: true, config: "dns4.json", next: "adopt", address: "dhcp", from: "config" }] }) }));
  await page.route(/\/api\/adopt$/, r => r.fulfill({ contentType: "application/json", body: JSON.stringify({ nodes: [],
    configs: [{ name: "dns4.json", config_name: "dns4", address: "192.0.2.253/23", gateway: "192.0.2.1" }],
    primary: {}, key: true, no_dhcp: noDHCP, dry_run_valid_s: 900 }) }));
}

// A node on DHCP: DHCP, reserved in the router, is chosen; the config is saved on DHCP (no
// gateway, not its static address) and the adoption is sent dhcp, reserved.
test("+ Add node: a node on DHCP stays on DHCP, reserved in the router", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await farNode(page, state);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  const go = list.getByRole("button", { name: "Add node" });
  await expect(list.locator("#nnname")).toHaveValue("dns4.json");
  await expect(list.locator("#nnhow-dhcp")).toBeChecked();
  await expect(list.locator("#nnip")).toBeHidden();
  await expect(list).toContainText(`Reserve ${FOUND} for its MAC (copy it above) in your router first`);
  await expect(list).toContainText(MACFOUND);
  await expect(go).toBeEnabled();
  await go.click();
  await expect(list).toContainText("a test: no adoption here");
  expect(state.posts.filter(p => p[0] === "config").map(p => [p[1], p[2].hash, JSON.parse(p[2].text)])).toEqual(
    [["dns4.json", "h1", { name: "dns4", network: { address: "dhcp" } }]]);
  expect(state.posts.filter(p => p[0] === "preview").map(p => p[1])).toEqual(
    [{ node: FOUND, node_id: "aa:bb:cc:00:00:37", config: "dns4.json", address: "dhcp", reserved: true, static: true }]);
});

// A fixed address on the node's own subnet, with a config made for another: the Gateway box
// shows the config's gateway, marked, and Add node waits until the address and gateway go
// together; then both are saved to the config and sent.
test("+ Add node: a fixed address, and the config's gateway from another subnet put right", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await farNode(page, state);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  const ip = list.locator("#nnip"), gw = list.locator("#nngw"), go = list.getByRole("button", { name: "Add node" });
  const hint = list.locator("#nnip-hint"), gwHint = list.locator("#nngw-hint");
  await expect(list.locator("#nnhow-dhcp")).toBeChecked();
  await list.locator("#nnhow-fixed").check();
  await expect(ip).toBeVisible();
  await expect(ip).toHaveValue("");
  await expect(gw).toHaveValue("192.0.2.1"); // the config's
  await expect(go).toBeDisabled();
  for (const bad of ["dhcp", FOUND, `${FOUND}/0`, `${FOUND}/33`, "198.51.100.256/24"]) {
    await ip.fill(bad);
    await expect(go).toBeDisabled();
    await expect(hint).toHaveClass(/\bbad\b/);
  }
  await ip.fill(`${FOUND}/24`);
  await expect(hint).not.toHaveClass(/\bbad\b/);
  await expect(go).toBeDisabled();
  await expect(gwHint).toHaveClass(/\bbad\b/);
  await expect(gwHint).toHaveText("192.0.2.1 isn't in the node's own network, 198.51.100.0/24. The gateway has to be in that network.");
  await gw.fill("");
  await expect(go).toBeDisabled();
  await gw.fill("198.51.100.1");
  await expect(gwHint).not.toHaveClass(/\bbad\b/);
  await expect(go).toBeEnabled();
  // Re-checked as the address changes.
  await ip.fill("198.51.101.20/24");
  await expect(gwHint).toHaveClass(/\bbad\b/);
  await expect(go).toBeDisabled();
  await ip.fill(`${FOUND}/23`);
  await expect(go).toBeEnabled();
  await go.click();
  await expect(list).toContainText("a test: no adoption here");
  expect(state.posts.filter(p => p[0] === "config").map(p => [p[1], p[2].hash, JSON.parse(p[2].text)])).toEqual(
    [["dns4.json", "h1", { name: "dns4", network: { address: `${FOUND}/23`, gateway: "198.51.100.1" } }]]);
  expect(state.posts.filter(p => p[0] === "preview").map(p => p[1])).toEqual(
    [{ node: FOUND, node_id: "aa:bb:cc:00:00:37", config: "dns4.json", address: `${FOUND}/23`, gateway: "198.51.100.1", static: true }]);
});

// No DHCP server on the node's network (settings.json no_dhcp): DHCP isn't offered, and it
// gets a fixed address.
test("+ Add node: no DHCP choice on a no_dhcp network", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await farNode(page, state, ["198.51.100.0/24"]);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  await expect(list.locator("#nnname")).toHaveValue("dns4.json");
  await expect(list.locator("#nnhow-dhcp")).toHaveCount(0);
  await expect(list.locator("#nnnodhcp")).toHaveText("198.51.100.0/24 has no DHCP server (settings): it gets a fixed address.");
  await expect(list.locator("#nnhow-fixed")).toBeChecked();
  await expect(list.locator("#nnip")).toBeVisible();
  await expect(list.getByRole("button", { name: "Add node" })).toBeDisabled();
});

// A name no node uses can be deleted from the panel, once confirmed; it is then gone from
// the names. One in use has no Delete.
test("+ Add node: Delete this name, for a config no node uses", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  const name = list.locator("#nnname");
  await expect(name).toHaveValue("dns4.json");
  await list.locator("#nndel").click();
  await expect(list.locator("#nndelask")).toContainText("Delete dns4.json? No node runs it. It is kept in its history.");
  await list.locator("#nndelask").getByRole("button", { name: "Cancel" }).click();
  await expect(list.locator("#nndelask")).toHaveCount(0);
  expect(state.posts.filter(p => p[0] === "delete")).toEqual([]);
  await list.locator("#nndel").click();
  await list.locator("#nndelask").getByRole("button", { name: "Delete dns4.json" }).click();
  await expect(list.locator('#nnname option[value="dns4.json"]')).toHaveCount(0);
  await expect(name).toHaveValue("+new"); // dns2's is in use: a new name is what is left
  await expect(list.locator("#nndel")).toHaveCount(0);
  expect(state.posts.filter(p => p[0] === "delete")).toEqual([["delete", "dns4.json", { hash: "h1" }]]);
  expect(state.files["dns4.json"]).toBeUndefined();
});

test("+ Add node: a lookup answered after the address was typed over is dropped", async ({ page }) => {
  await login(page);
  await fake(page);
  // The lookup answers late: by then the box holds other text.
  await page.route(/\/api\/nodes\/lookup$/, async r => { await new Promise(res => setTimeout(res, 800)); await r.fallback(); });
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  const box = page.locator("#nq"), list = page.locator("#nqres");
  await box.fill(TYPED);
  await box.press("Enter");
  await expect(list).toContainText(`Looking for a node at ${TYPED}`);
  await box.fill("dns");
  await page.waitForTimeout(1500);
  await expect(list.locator("#nnname")).toHaveCount(0);
  await expect(list).toContainText("Type the node's whole address");
});

test("an apply the controller no longer has doesn't stay Applying", async ({ page }) => {
  await login(page);
  await fake(page);
  await page.route(/\/api\/jobs\/j1\/events/, r => r.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: "no such job" }) }));
  await page.goto("./");
  const chip = page.locator(".changes .chip-btn");
  await chip.click();
  await page.getByRole("dialog", { name: "Changes to apply" }).getByRole("button", { name: "Apply 1 change" }).click();
  const d = page.getByRole("dialog");
  await expect(d).toContainText("Apply failed");
  await expect(d).toContainText("no longer has this apply");
  await expect(d.getByRole("button", { name: "Stop after this node" })).toHaveCount(0);
  await d.getByRole("button", { name: "Done" }).click();
  await expect(chip).toHaveText("1 change to apply");
});

test("an apply with its changes gone meanwhile closes the list and says so", async ({ page }) => {
  await login(page);
  const state = await fake(page);
  // The one change was an update for a node now on its version: the controller dropped it.
  await page.route(/\/api\/jobs$/, r => {
    if (r.request().method() !== "POST") return r.fallback();
    state.changes = [];
    return r.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: "nothing to apply: no pending changes" }) });
  });
  await page.goto("./");
  const chip = page.locator(".changes .chip-btn");
  await chip.click();
  await page.getByRole("dialog", { name: "Changes to apply" }).getByRole("button", { name: "Apply 1 change" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.locator("#toast")).toContainText("nothing to apply");
  await expect(chip).toHaveCount(0);
});

test("the pending changes show in the header, and Apply runs them", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  const chip = page.locator(".changes .chip-btn");
  await expect(chip).toHaveText("1 change to apply");
  await chip.click();
  const drawer = page.getByRole("dialog", { name: "Changes to apply" });
  await expect(drawer).toBeVisible();
  await expect(drawer).toContainText("Always allow example.com");
  await expect(drawer).toContainText("No restart needed");
  await drawer.getByRole("button", { name: "Apply 1 change" }).click();
  await expect(page.getByRole("dialog")).toContainText("Changes applied");
  await expect(page.getByRole("dialog")).toContainText("dns3");
  const job = state.posts.find(p => p[0] === "job");
  expect(job[1].kind).toBe("apply");
  expect(job[1].params.soak_s).toBe(60);
  await page.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(chip).toHaveCount(0);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a panel closes with its x, Esc and a click outside", async ({ page }) => {
  await login(page);
  await fake(page);
  await page.goto("./");
  const chip = page.locator(".changes .chip-btn");
  for (const how of ["x", "Escape", "outside"]) {
    await chip.click();
    const d = page.getByRole("dialog", { name: "Changes to apply" });
    await expect(d).toBeVisible();
    if (how === "x") await d.getByRole("button", { name: "Close" }).click();
    else if (how === "Escape") await page.keyboard.press("Escape");
    else await page.mouse.click(10, 300);
    await expect(d).toHaveCount(0);
  }
});

// A node's MAC: on hovering its address in its row (found or listed; no MAC to copy there),
// and to copy in + Add node's view of the node chosen or looked up.
test("the MAC: on its row's address, and to copy in + Add node", async ({ page, context }) => {
  const violations = await watch(page);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page);
  await fake(page);
  await page.goto("./");
  const rows = page.locator("#nodes .r:not(.headr)");
  await expect(rows).toHaveCount(3);
  const dns2 = rows.nth(0), dns3 = rows.nth(1), found = rows.nth(2);
  await expect(dns2.locator(".ip")).toHaveAttribute("title", `MAC ${MAC2}`);
  await expect(dns3.locator(".ip")).toHaveAttribute("title", `MAC ${MAC3}`);
  await expect(found.locator(".ip")).toHaveAttribute("title", `MAC ${MACFOUND}`);
  await expect(page.locator("#nodes .mac")).toHaveCount(0);
  const clip = () => page.evaluate(() => navigator.clipboard.readText());

  // The address's copy button copies the address, and doesn't open the row.
  const navs = [];
  page.on("framenavigated", f => { if (f === page.mainFrame()) navs.push(f.url()); });
  await found.getByRole("button", { name: `Copy ${FOUND}` }).click();
  expect(await clip()).toBe(FOUND);
  await dns3.getByRole("button", { name: `Copy ${DNS3}` }).click();
  expect(await clip()).toBe(DNS3);
  await page.waitForLoadState("networkidle");
  expect(navs).toEqual([]);
  await expect(page).toHaveURL(/\/$|index\.html$/);

  // + Add node: the found node chosen, its MAC with the hint.
  await page.getByRole("button", { name: "+ Add node" }).click();
  const list = page.locator("#nqres");
  await list.getByRole("button", { name: "Add" }).click();
  await expect(list.locator("#nnname")).toHaveValue("dns4.json");
  await expect(list.locator(".macl")).toContainText("For a DHCP reservation in your router, or to set a static address.");
  await list.getByRole("button", { name: `Copy MAC ${MACFOUND}` }).click();
  expect(await clip()).toBe(MACFOUND);
  // and one looked up at its address.
  await list.getByRole("button", { name: "Back" }).click();
  await page.locator("#nq").fill(TYPED);
  await page.locator("#nq").press("Enter");
  await expect(list.locator(".macl .mac .ip")).toHaveText(MACTYPED);
  await list.getByRole("button", { name: `Copy MAC ${MACTYPED}` }).click();
  expect(await clip()).toBe(MACTYPED);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// A node found with firmware from before 0.0.1 (/status version "1": it takes no node
// configs). Nothing adds it over the network: it is updated over USB first.
const OLD = "198.51.100.38", MACOLD = "aa:bb:cc:00:00:38";
const oldNode = { host: OLD, id: "aa:bb:cc:00:00:38", hostname: "espdns-000038.local", source: "mdns", online: true, read: true,
  board: "xiao-s3-sense", image: "esp32s3-octal", version: "1", net: "wifi", mac: MACOLD, adopted: false, listed: false, configs: false,
  refusals: ["its firmware takes no node configs: update it first"], next: "", install: "usb" };

// fakeOld adds the old node to the found ones, and the chip image a USB install gives.
async function fakeOld(page) {
  const json = (route, body) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/nodes\/found$/, async r => {
    return json(r, { nodes: [{ host: FOUND, id: "aa:bb:cc:00:00:37", hostname: "espdns-000037.local", source: "mdns", online: true, read: true,
      board: "xiao-s3-sense", image: "esp32s3-octal", version: "0.0.4", net: "wifi", mac: MACFOUND, adopted: false, listed: false, configs: true,
      config: "dns4.json", next: "adopt" }, oldNode] });
  });
  await page.route(/\/api\/updates$/, r => json(r, { scheme: "version", available: [],
    builds: [{ source: "images/esp32s3-octal", image: "esp32s3-octal", version: "0.0.1", build: "c1" }], nodes: [] }));
}

// usbPanel is Install over USB opened for the old node: its board chosen, its address filled in.
async function usbPanel(page) {
  const u = page.getByRole("dialog", { name: "Update Seeed XIAO ESP32S3 Sense over USB" });
  await expect(u).toBeVisible();
  await expect(u.locator("#usb-board")).toHaveValue("xiao-s3-sense");
  await expect(u.locator("#usb-facts")).toContainText("esp32s3-octal");
  await expect(u.locator("#usb-addr")).toHaveValue(`${OLD}/24`);
  return u;
}

test("a found node on old firmware: its row opens its setup, Update over USB opens Install over USB on its board", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await fakeOld(page);
  await page.goto("./");
  const rows = page.locator("#nodes .r:not(.headr)");
  await expect(rows).toHaveCount(4);
  const old = rows.nth(3);
  await expect(old).toContainText("Firmware older than 0.0.1: update it over USB first");
  await expect(old).not.toContainText("Can't be added yet");
  await expect(old.getByRole("button", { name: "Update over USB" })).toBeVisible();

  // A click anywhere on the row (its state, not a button) opens its setup panel.
  await old.locator(".flag").click();
  const d = page.getByRole("dialog", { name: "Seeed XIAO ESP32S3 Sense" });
  await expect(d).toBeVisible();
  await expect(d).toContainText(OLD);
  await expect(d).toContainText(MACOLD);
  await expect(d).toContainText("Its firmware is older than 0.0.1; installing 0.0.1 over USB erases it.");
  await expect(d).toContainText("Once it runs 0.0.1 or later, it shows here again, ready to add.");
  await expect(d.getByRole("button", { name: "Add node" })).toHaveCount(0);
  await expect(page).toHaveURL(/\/$|index\.html$/);

  // Update over USB: Install over USB, its board chosen, on this page.
  await d.getByRole("button", { name: "Update over USB" }).click();
  await usbPanel(page);
  await expect(page).toHaveURL(/\/$|index\.html$/);
  // ... and from the row's own button.
  await page.keyboard.press("Escape");
  await old.getByRole("button", { name: "Update over USB" }).click();
  await usbPanel(page);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a found node ready to add: its row opens its setup, and Add node there opens the Add flow with it", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  const found = page.locator("#nodes .r.r-new").first();
  await found.locator(".flag").click();
  const d = page.getByRole("dialog");
  await expect(d).toContainText("Add it to your nodes");
  await d.getByRole("button", { name: "Add node" }).click();
  await expect(d).toHaveCount(0);
  await expect(page.locator("#nqres #nnname")).toHaveValue("dns4.json");
  await expect(page.locator("#nqres")).toContainText(`Found at ${FOUND}`);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// Every item in + Add node's list acts on a real mouse click on it (not only its button), and
// on Enter: nothing in the list is dead.
test("+ Add node: every item acts on a click and on Enter", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await fakeOld(page);
  await page.goto("./");
  const add = page.getByRole("button", { name: "+ Add node" });
  const list = page.locator("#nqres");
  const items = list.locator(".opt.pick");
  const clickAt = async loc => { const b = await loc.boundingBox(); await page.mouse.click(b.x + 12, b.y + b.height / 2); };

  // The node ready to add: its setup panel, then the Add flow with it.
  await add.click();
  await expect(items).toHaveCount(2);
  await clickAt(items.nth(0));
  let d = page.getByRole("dialog");
  await expect(d).toContainText("Add it to your nodes");
  await d.getByRole("button", { name: "Add node" }).click();
  await expect(list.locator("#nnname")).toHaveValue("dns4.json");
  await page.locator("#nq").press("Escape");

  // The one on old firmware: its setup panel, Update over USB.
  await add.click();
  await expect(items.nth(1)).toContainText("Firmware older than 0.0.1: update it over USB first");
  await clickAt(items.nth(1));
  d = page.getByRole("dialog", { name: "Seeed XIAO ESP32S3 Sense" });
  await expect(d.getByRole("button", { name: "Update over USB" })).toBeVisible();
  await d.getByRole("button", { name: "Close" }).first().click();
  await expect(d).toHaveCount(0);
  // Its own button: Install over USB at once.
  await add.click();
  await items.nth(1).getByRole("button", { name: "Update over USB" }).click();
  await usbPanel(page);
  await page.keyboard.press("Escape");

  // Enter on an item does the same.
  await add.click();
  await items.nth(1).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog", { name: "Seeed XIAO ESP32S3 Sense" })).toBeVisible();
  await page.keyboard.press("Escape");

  // A typed address is looked up.
  await add.click();
  await page.locator("#nq").fill(TYPED);
  await page.locator("#nq").press("Enter");
  await expect(list.locator("#nnname")).toHaveValue("dns4.json");
  await page.locator("#nq").fill("");
  await page.locator("#nq").press("Escape");

  // No new board in the list: only the nodes found.
  await add.click();
  await expect(items).toHaveCount(2);
  await page.locator("#nq").press("Escape");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// An older version is an update, whatever the chip: a button in its row, opening its panel
// (not the node's page) with the versions, nothing held for its chip.
test("an older version: an update, whatever the chip", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.route(/\/api\/updates$/, r => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ scheme: "version", newest: "0.0.2", builds: [], available: [DNS2], nodes: [
    { host: DNS2, listed: true, online: true, image: "esp32s3-octal", runs: { version: "0.0.1", build: "a1" }, newest: { version: "0.0.2" },
      state: "available", why: "Update available: 0.0.1 → 0.0.2" },
  ] }) }));
  await page.goto("./");
  const dns2 = page.locator("#nodes .r:not(.headr)").nth(0);
  const b = dns2.getByRole("button", { name: "Update dns2 from 0.0.1 to 0.0.2" });
  await expect(b).toHaveText("Update 0.0.1 → 0.0.2");
  await b.focus();
  await page.keyboard.press("Enter");
  const d = page.getByRole("dialog", { name: "Update firmware" });
  await expect(d.getByRole("checkbox", { name: "Update dns2" })).toBeChecked();
  await expect(d).toContainText("0.0.1 → 0.0.2");
  await expect(page).not.toHaveURL(/node\.html/);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// Two nodes out of date: a button in each row, its node alone ticked, and Update all by
// + Add node, every one ticked; one waiting to apply says so in its row.
test("two updates: one in each row, Update all in the head", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  const node = (host, v, more) => ({ host, listed: true, online: true, runs: { version: v }, newest: { version: "0.0.4" }, state: "available", why: "", ...more });
  let nodes = [node(DNS2, "0.0.1"), node(DNS3, "0.0.2")];
  await page.route(/\/api\/updates$/, r => r.fulfill({ status: 200, contentType: "application/json",
    body: JSON.stringify({ scheme: "version", builds: [], available: [DNS2, DNS3], nodes }) }));
  await page.goto("./");
  const rows = page.locator("#nodes .r:not(.headr)");
  const all = page.getByRole("button", { name: "Update all" });
  await expect(all).toBeVisible();
  await rows.nth(0).getByRole("button", { name: "Update dns2 from 0.0.1 to 0.0.4" }).click();
  let d = page.getByRole("dialog", { name: "Update firmware" });
  await expect(d.getByRole("checkbox", { name: "Update dns2" })).toBeChecked();
  await expect(d.getByRole("checkbox", { name: "Update dns3" })).not.toBeChecked();
  await d.getByRole("button", { name: "Not now" }).click();
  await all.click();
  d = page.getByRole("dialog", { name: "Update firmware" });
  await expect(d.getByRole("checkbox", { name: "Update dns2" })).toBeChecked();
  await expect(d.getByRole("checkbox", { name: "Update dns3" })).toBeChecked();
  await expect(d).toContainText("One node at a time restarts; the others keep answering.");
  await d.getByRole("button", { name: "Not now" }).click();

  // dns2's waiting to apply: words in its row, and one left, so no Update all.
  nodes = [node(DNS2, "0.0.1", { pending: true }), node(DNS3, "0.0.2")];
  await page.reload();
  await expect(rows.nth(0)).toContainText("Update waiting to apply: 0.0.1 → 0.0.4");
  await expect(rows.nth(0).locator(".upd")).toHaveCount(0);
  await expect(rows.nth(1).locator(".upd")).toBeVisible();
  await expect(all).toBeHidden();

  // dns2 got 0.0.4 another way (over USB) while its update waited: no update in its row.
  nodes = [node(DNS2, "0.0.4", { pending: true, state: "current" }), node(DNS3, "0.0.2")];
  await page.reload();
  await expect(rows.nth(1).locator(".upd")).toBeVisible();
  await expect(rows.nth(0)).not.toContainText("Update");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 900 });
      await login(page);
      await fake(page);
      await page.goto("./");
      await expect(page.locator("#minis .spark")).toHaveCount(3);
      await page.screenshot({ path: path.join(shots, `nodes-${width}-${scheme}.png`), fullPage: true });
      await page.getByRole("button", { name: "+ Add node" }).click();
      await expect(page.locator("#nqres")).toContainText(FOUND);
      await page.screenshot({ path: path.join(shots, `add-node-${width}-${scheme}.png`), fullPage: true });
      await page.locator("#nqres").getByRole("button", { name: "Add" }).click();
      await expect(page.locator("#nnname")).toHaveValue("dns4.json");
      await page.screenshot({ path: path.join(shots, `add-node-form-${width}-${scheme}.png`), fullPage: true });
      await page.locator("#nq").press("Escape");
      await page.locator(".changes .chip-btn").click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.screenshot({ path: path.join(shots, `changes-${width}-${scheme}.png`) });
    });
  }
}
