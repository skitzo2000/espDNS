// The Zones page (zones.html, zones.js) against a fake controller: serve.sh's controller
// logs in and serves the pages, and the APIs the page reads and writes are answered here
// (page.route), with documentation addresses (RFC 5737) and names (RFC 2606), so nothing
// goes on the network. The fake keeps the pending changes and the configs they make, as
// the controller does. E2E_SHOTS=<dir> also takes the page, + Add zone, a zone's page and
// its panels at 375 and 1366 px, light and dark, into <dir>/zones.
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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", PRIMARY = "192.0.2.254", SOURCE = "192.0.2.5", FWD = "198.51.100.53", FWD2 = "198.51.100.54";

function node(addr, id, name) {
  return { id, addr, hostname: `${name}.local`, source: "settings", online: true, last_seen: new Date().toISOString(), qps: 0.4,
    status: { board: "p4-ip101", version: "0.0.4", uptime_s: 9660, net: { kind: "ethernet" }, health: { state: "healthy", answering: true, reasons: [] },
      config: { name, source: "node" } } };
}

const HOME = "$TTL 3600\n@ IN SOA ns1.home.example. hostmaster.home.example. ( 2026100701 3600 600 604800 300 )\n  IN NS ns1\nns1 IN A 192.0.2.53\nnas IN A 192.0.2.10\n";
const HOMERECS = [
  { owner: "home.example.", type: "SOA", ttl: 3600, data: "ns1.home.example. hostmaster.home.example. 2026100701 3600 600 604800 300" },
  { owner: "home.example.", type: "NS", ttl: 3600, data: "ns1.home.example." },
  { owner: "nas.home.example.", type: "A", ttl: 3600, data: "192.0.2.10" },
  { owner: "ns1.home.example.", type: "A", ttl: 3600, data: "192.0.2.53" },
];

// fake answers the page's APIs. state.changes is what /api/changes lists; state.posts what
// was sent; state.configs the node configs as the pending changes make them; state.primary
// the zone primary in the settings: its address, the networks it allows, the zones to copy.
async function fake(page) {
  const cfg = name => JSON.stringify({ name, secondary: { primary: PRIMARY, zones: ["lab.example", "stale.example"] },
    forward_zones: [{ zone: "corp.example", forwarder: FWD }], wifi: { ssid: "home", password: "••••••••" } }, null, 2);
  const state = { changes: [], posts: [], configs: { "dns2.json": cfg("dns2"), "dns3.json": cfg("dns3") }, zone: HOME, n: 0,
    primary: { address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["lab.example"] }, version: 1 };
  const inv = {
    zones: [
      { name: "corp.example", kind: "forward", forwarders: [FWD], configs: ["dns2.json", "dns3.json"], serving: 2, nodes: 2, state: "ok", text: "on every node" },
      { name: "home.example", kind: "hosted", file: "home.example.zone", serial: 2026100701, records: 4, serving: 2, nodes: 2, state: "ok", text: "on every node, as saved" },
      { name: "lab.example", kind: "secondary", primary: PRIMARY, serial: 7, records: 12, configs: ["dns2.json", "dns3.json"], serving: 2, nodes: 2, state: "ok", text: "on every node" },
      { name: "stale.example", kind: "secondary", primary: PRIMARY, serial: 3, records: 5, configs: ["dns2.json", "dns3.json"], serving: 2, nodes: 2, state: "expired",
        text: "2 nodes' copies expired: it can't copy the zone from its primary" },
    ],
    counts: { hosted: 1, secondary: 2, forward: 1, records: 21 }, primaries: [PRIMARY], nodes: 2, fleet: 2,
    primary: { set: true, address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["lab.example"] },
  };
  const settings = () => ({ settings: { nodes: [DNS2, DNS3], dns_peers: [], dns_peer_zones: [], canary: "", no_dhcp: [], primary: state.primary, internal_sources: [] },
    version: `v${state.version}`, exists: true, primary: { ...inv.primary, uncovered: [] } });
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  const hash = t => `h${t.length}`;
  await page.route(/\/api\//, route => {
    const req = route.request(), u = new URL(req.url()), p = u.pathname.replace(/^\/api\//, ""), m = req.method();
    const q = k => u.searchParams.get(k);
    const body = () => req.postDataJSON();
    if (m === "GET" && p === "nodes") return json(route, [node(DNS2, "aa:bb:cc:00:00:02", "dns2"), node(DNS3, "aa:bb:cc:00:00:03", "dns3")]);
    if (m === "GET" && p === "zone-inventory") return json(route, inv);
    if (m === "GET" && p === "configs") return json(route, { configs: Object.keys(state.configs).map(name => ({ name, nodes: [{ host: name === "dns2.json" ? DNS2 : DNS3, listed: true }] })), nodes: [] });
    if (m === "GET" && p === "changes/file") {
      if (q("kind") === "config") { const t = state.configs[q("name")]; return json(route, { kind: "config", name: q("name"), exists: !!t, text: t, hash: hash(t || "") }); }
      if (q("kind") === "zone") {
        if (q("name") === "home.example.zone") return json(route, { kind: "zone", name: q("name"), exists: true, text: state.zone, hash: hash(state.zone) });
        const c = state.changes.find(x => x.kind === "zone" && x.name === q("name"));
        return json(route, c ? { kind: "zone", name: q("name"), exists: true, pending: true, text: c.text, hash: hash(c.text) } : { kind: "zone", name: q("name"), exists: false, hash: "" });
      }
    }
    if (m === "GET" && p === "changes") return json(route, { changes: state.changes.map(({ text, ...c }) => c), restarts: [], apply: null });
    if (m === "POST" && p === "changes") {
      const b = body();
      state.posts.push(["change", b]);
      const c = { id: `c${++state.n}`, kind: b.kind, name: b.name, delete: !!b.delete, summary: b.summary, effect: b.kind === "zone" ? "Hosted zones: sent to every node, live (no restart)" : "dns2: live (no restart)", text: b.text };
      if (b.kind === "config") state.configs[b.name] = b.text;
      if (b.kind === "zone" && b.name === "home.example.zone" && !b.delete) state.zone = b.text;
      state.changes.push(c);
      return json(route, c, 201);
    }
    let dm = p.match(/^changes\/(c\d+)\/discard$/);
    if (m === "POST" && dm) {
      state.posts.push(["discard", dm[1]]);
      const c = state.changes.find(x => x.id === dm[1]);
      if (!c) return json(route, { error: "no change " + dm[1] }, 404);
      if (c.kind === "config") state.configs[c.name] = cfg(c.name.replace(".json", ""));
      state.changes = state.changes.filter(x => x !== c);
      return json(route, { ids: [c.id] });
    }
    if (m === "GET" && p === "zone-lookup") {
      const name = q("name").toLowerCase().replace(/\.$/, "");
      const tried = [{ primary: PRIMARY, answer: "refused: it doesn't serve this zone" }];
      if (name === "home.example") return json(route, { name, found: "zone", zone: inv.zones[1], tried: [], text: "" });
      if (name === "www.home.example") return json(route, { name, found: "within", zone: inv.zones[1], parent: "home.example", tried: [], text: "" });
      if (name === "iot.example") return json(route, { name, found: "primary", primary: PRIMARY, serial: 42, tried: [{ primary: PRIMARY, answer: "serves it (serial 42)" }], text: "" });
      return json(route, { name, found: "none", tried, text: "" });
    }
    if (m === "GET" && p === "zone-records") {
      if (q("name") !== "lab.example" || state.refuse) return json(route, { error: `your primary DNS server (${PRIMARY}) refused to send this zone to the controller: allow zone transfers from this computer's address on it`,
        kind: "refused", primary: PRIMARY, source: SOURCE, detail: "rcode 5 (REFUSED)" }, 502);
      return json(route, { name: "lab.example", primary: PRIMARY, serial: 7, more: 0, records: [
        { owner: "printer.lab.example.", type: "A", ttl: 300, data: "192.0.2.45" },
        { owner: "lab.example.", type: "SOA", ttl: 3600, data: "ns1.lab.example. hostmaster.lab.example. 7 3600 600 604800 300" },
        { owner: "nas.lab.example.", type: "A", ttl: 300, data: "192.0.2.10" },
        { owner: "lab.example.", type: "TXT", ttl: 300, data: '"' + "v=example1 " + "k".repeat(120) + '"' },
        { owner: "lab.example.", type: "NS", ttl: 3600, data: "ns1.lab.example." }] });
    }
    dm = p.match(/^zones\/([^/]+)\/(check|serial|new)$/);
    if (dm && dm[2] === "check") {
      const t = body().text;
      const recs = HOMERECS.filter(r => t.includes(r.data.split(" ")[0]) || r.type === "SOA");
      for (const l of t.split("\n")) {
        const f = l.trim() && !/^\s/.test(l) ? l.split(/\s+/) : [];
        const ttl = /^\d+$/.test(f[1] || "") ? Number(f.splice(1, 1)[0]) : 3600;
        const data = f.slice(3).join(" ");
        if (f.length >= 4 && f[1] === "IN" && f[2] !== "SOA" && !recs.some(r => r.data === data)) recs.push({ owner: `${f[0]}.home.example.`, type: f[2], ttl, data });
      }
      return json(route, { ok: true, zone: "home.example", records: recs.length, list: recs, lines: ["1 zone, fits"], nodes: [] });
    }
    if (dm && dm[2] === "serial") return json(route, { text: body().text.replace("2026100701", "2026100702"), serial: 2026100702, was: 2026100701 });
    if (dm && dm[2] === "new") return json(route, { name: decodeURIComponent(dm[1]), text: `$TTL 3600\n@ IN SOA ns1 hostmaster ( 2026100701 3600 600 604800 300 )\n  IN NS ns1\n` });
    if (m === "GET" && p === "zones/home.example.zone") {
      if (q("version")) return json(route, { name: "home.example.zone", version: q("version"), text: HOME.replace("nas IN A 192.0.2.10\n", "") });
      return json(route, { name: "home.example.zone", text: state.zone, hash: hash(state.zone), history: [{ file: "home.example.zone.1", time: "2026-10-01T10:00:00Z", size: 120 }] });
    }
    if (m === "GET" && p === "settings") return json(route, settings());
    if (m === "POST" && p === "settings") {
      const b = body();
      state.posts.push(["settings", b]);
      if (b.version !== `v${state.version}`) return json(route, { error: "the settings changed meanwhile" }, 409);
      const np = b.settings.primary || {};
      if (Object.keys(np).some(k => !["address", "allows", "zones"].includes(k))) return json(route, { error: "primary: unknown field", field: "settings" }, 400);
      state.primary = np;
      inv.primary = { set: !!b.settings.primary, address: np.address || "", allows: np.allows || [], zones: np.zones || [] };
      state.version++;
      return json(route, settings());
    }
    if (m === "POST" && p === "jobs") {
      const b = body();
      state.posts.push(["job", b]);
      return json(route, { id: "j1", kind: b.kind, who: "admin", state: "running", created: new Date().toISOString() }, 201);
    }
    if (p === "jobs/j1/events") {
      const end = { id: "j1", kind: "primary", who: "admin", state: "done" };
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: `id: 1\nevent: end\ndata: ${JSON.stringify(end)}\n\n` });
    }
    if (["updates", "nodes/found", "dashboard"].includes(p)) return json(route, {});
    return route.fallback();
  });
  return state;
}

// The zone primary: its address, the networks it allows zone transfers from (the nodes in
// none of them named, with the network to allow), and the zones copied from it: one added
// is copied by every node, one removed no longer is, each a change to apply.
test("Zones: the zone primary's address, networks and zones", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  state.primary = { address: PRIMARY, allows: ["198.51.100.0/24"], zones: ["lab.example"] };
  await page.goto("zones.html");
  const row = page.locator("#primary-row");
  await expect(row.locator("summary")).toContainText(`${PRIMARY} · allows 192.0.2.0/24`);
  await row.locator("summary").click();
  await expect(page.locator("#pa")).toHaveValue(PRIMARY);
  await expect(page.locator("#pn-list li")).toHaveText([/198\.51\.100\.0\/24/]);
  // Both nodes outside the networks it allows: said, with the network to allow.
  await expect(page.locator("#pn-uncovered")).toHaveText(`Not in these networks: ${DNS2}, ${DNS3}. Allow their network on your primary (e.g. 192.0.2.0/24), then add it here.`);
  await expect(page.locator("#pn-uncovered")).toHaveClass(/\bbad\b/);
  // The zones the nodes copy from it, listed.
  await expect(page.locator("#pz-list li .mono")).toHaveText(["lab.example", "stale.example"]);
  // No API, token or certificate.
  await expect(row).not.toContainText(/token|certificate|Test connection|Kind/i);

  // A network that isn't one: refused, said in place.
  await page.locator("#pn").fill("192.0.2.1");
  await page.locator("#pn-add").click();
  await expect(row.locator(".hint.bad").filter({ hasText: "isn't a network" })).toBeVisible();
  await page.locator("#pn").fill("192.0.2.77/24");
  await page.locator("#pn").press("Enter");
  await expect(page.locator("#pn-list li .mono")).toHaveText(["198.51.100.0/24", "192.0.2.0/24"]);
  await expect(page.locator("#pn-uncovered")).toHaveText("Every node's address is in these networks.");
  await page.getByRole("button", { name: "Remove 198.51.100.0/24" }).click();
  // A zone added, one removed.
  await page.locator("#pz").fill("IoT.Example.");
  await page.locator("#pz-add").click();
  await page.getByRole("button", { name: "Remove stale.example" }).click();
  await expect(page.locator("#pz-list li .mono")).toHaveText(["lab.example", "iot.example"]);
  await page.locator("#psave").click();
  await expect(row).toContainText("Saved. The zones' changes wait in \"changes to apply\".");
  const saved = state.posts.filter(([k]) => k === "settings").map(([, b]) => b.settings.primary);
  expect(saved[0]).toEqual({ address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["lab.example", "iot.example"] });
  expect(state.primary.zones).toEqual(["lab.example", "iot.example"]);
  const cs = changePosts(state, "config");
  expect(cs.length).toBe(4);
  for (const c of cs.slice(2)) expect(JSON.parse(c.text).secondary.zones).toEqual(["lab.example", "iot.example"]);
  await expect(row.locator("summary")).toContainText(`${PRIMARY} · allows 192.0.2.0/24`);

  // An address that isn't one: refused before anything is sent.
  const n = state.posts.length;
  await page.locator("#pa").fill("primary.example");
  await page.locator("#psave").click();
  await expect(row.locator(".hint.bad").filter({ hasText: "isn't an IPv4 address" })).toBeVisible();
  expect(state.posts.length).toBe(n);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

const changePosts = (state, kind) => state.posts.filter(([k, b]) => k === "change" && b.kind === kind).map(([, b]) => b);

test("Zones: every zone in one place, with the fleet's state", async ({ page }) => {
  const violations = await watch(page);

  await login(page);
  await fake(page);
  await page.goto("zones.html");
  const nav = page.locator('nav[aria-label="Main"] a');
  await expect(nav.nth(1)).toHaveAttribute("aria-current", "page");
  await expect(page.locator("h1")).toHaveText("Zones");
  const rows = page.locator("#zones .r:not(.headr)");
  await expect(rows).toHaveCount(4);
  await expect(rows.locator(".name")).toHaveText(["corp.example", "home.example", "lab.example", "stale.example"]);
  await expect(rows.nth(0)).toContainText(`Forwarded to ${FWD}`);
  await expect(rows.nth(0)).toContainText("Forwarding");
  await expect(rows.nth(1)).toContainText("Hosted here");
  await expect(rows.nth(1)).toContainText("edited here");
  await expect(rows.nth(2)).toContainText("Copied from your primary");
  await expect(rows.nth(2)).toContainText("version 7");
  await expect(rows.nth(2)).toContainText("Up to date");
  await expect(rows.nth(3)).toHaveClass(/\br-warn\b/);
  await expect(rows.nth(3)).toContainText("Can't copy it");
  // No node's state on the zones page.
  await expect(page.locator("#zones")).not.toContainText("dns3");
  await expect(page.locator("#facts")).toHaveText(/2 copied from your primary\s*1 forwarded\s*1 hosted here\s*21 records/);
  await expect(page.locator("#primary-row summary")).toContainText(`${PRIMARY} · allows 192.0.2.0/24`);
  await expect(page.locator("#copy-dns")).toHaveAttribute("title", `${DNS2}, ${DNS3}`);

  // The zone primary opens to its form.
  await page.locator("#primary-row summary").click();
  await expect(page.locator("#pa")).toHaveValue(PRIMARY);
  await expect(page.locator("#pn-uncovered")).toHaveText("Every node's address is in these networks.");
  await expect(page.locator("#primary-row")).not.toContainText("null");

  // A row opens its zone.
  await rows.nth(2).getByText("Copied from your primary").click();
  await expect(page).toHaveURL(/#zone=lab\.example$/);
  await expect(page.locator("h1")).toHaveText("lab.example");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("+ Add zone: looked up, then copied, hosted or forwarded as pending changes", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("zones.html");
  await page.getByRole("button", { name: "+ Add zone" }).click();
  const box = page.locator("#zq"), res = page.locator("#zqres");
  await expect(box).toBeFocused();
  await expect(res).toContainText("Type a zone");

  // One of the fleet's already; a name inside one.
  await box.fill("home.example");
  await expect(res).toContainText("Already one of your zones: hosted here.");
  await box.fill("www.home.example");
  await expect(res).toContainText("A name inside home.example");

  // On the primary: copied, into both nodes' configs.
  await box.fill("iot.example");
  await expect(res).toContainText(`Found on your primary at ${PRIMARY}, version 42`);
  await res.getByRole("button", { name: "Copy from your primary" }).click();
  await expect(page.locator("#zq")).toHaveCount(0);
  let cs = changePosts(state, "config");
  expect(cs.map(c => c.name).sort()).toEqual(["dns2.json", "dns3.json"]);
  for (const c of cs) {
    const cfg = JSON.parse(c.text);
    expect(cfg.secondary.zones).toEqual(["lab.example", "stale.example", "iot.example"]);
    expect(cfg.secondary.primary).toBe(PRIMARY);
    expect(cfg.wifi.password).toBe("••••••••"); // the hidden password, kept by the controller
    expect(c.summary).toMatch(/^Add zone iot\.example: copied from 192\.0\.2\.254/);
  }
  const iot = page.locator('#zones .r[data-zone="iot.example"]');
  await expect(iot).toContainText("Waiting to apply");
  await expect(iot).toContainText("after apply");
  await expect(page.locator(".changes .chip-btn")).toHaveText("2 changes to apply");

  // Forwarded, set up by hand.
  await page.getByRole("button", { name: "+ Add zone" }).click();
  await page.locator("#zq").fill("vpn.example");
  await expect(res).toContainText("Your primary doesn't have this zone");
  await res.getByText("Set it up myself").click();
  await res.locator("input[value=fwd]").check();
  await page.locator("#zmfwd").fill("not an address");
  await page.locator("#zmadd").click();
  await expect(res).toContainText("IPv4 address");
  await page.locator("#zmfwd").fill(FWD2);
  await page.locator("#zmadd").click();
  await expect(page.locator('#zones .r[data-zone="vpn.example"]')).toContainText(`Forwarded to ${FWD2}`);
  cs = changePosts(state, "config");
  expect(cs).toHaveLength(4);
  expect(JSON.parse(cs[3].text).forward_zones).toEqual([{ zone: "corp.example", forwarder: FWD }, { zone: "vpn.example", forwarder: FWD2 }]);

  // New: hosted here, from the template, then its page.
  await page.getByRole("button", { name: "+ Add zone" }).click();
  await page.locator("#zq").fill("new.example");
  await res.getByRole("button", { name: "Host it here" }).click();
  await expect(page).toHaveURL(/#zone=new\.example$/);
  const z = changePosts(state, "zone");
  expect(z).toHaveLength(1);
  expect(z[0]).toMatchObject({ name: "new.example.zone", hash: "", summary: "Add zone new.example: hosted here" });
  await expect(page.locator("#pending")).toContainText("this zone is added with the next Apply");

  // Undone: its change discarded, back to the list.
  await page.locator("#undo").click();
  await expect(page).not.toHaveURL(/#zone=/);
  expect(state.posts.filter(([k]) => k === "discard")).toHaveLength(1);
  await expect(page.locator('#zones .r[data-zone="new.example"]')).toHaveCount(0);

  // Esc closes the box.
  await page.getByRole("button", { name: "+ Add zone" }).click();
  await page.locator("#zq").press("Escape");
  await expect(page.getByRole("button", { name: "+ Add zone" })).toBeVisible();
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("A hosted zone: its records edited with forms, the file under Advanced", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("zones.html#zone=home.example");
  await expect(page.locator("h1")).toHaveText("home.example");
  const recs = page.locator(".recs tbody tr");
  await expect(recs).toHaveCount(4);
  await expect(recs.nth(2)).toContainText("nas");
  await expect(recs.nth(2)).toContainText("192.0.2.10");

  // + Add record: a panel; the record goes at the end of the file, the version raised.
  await page.locator("#add-record").click();
  const dlg = page.getByRole("dialog");
  await expect(dlg).toContainText("Add a record to home.example");
  await page.locator("#rname").fill("printer");
  await page.locator("#rvalue").fill("not an address");
  await page.locator("#rsave").click();
  await expect(dlg).toContainText("IPv4 address");
  await page.locator("#rvalue").fill("192.0.2.45");
  await page.locator("#rsave").click();
  await expect(dlg).toHaveCount(0);
  let z = changePosts(state, "zone");
  expect(z).toHaveLength(1);
  expect(z[0].name).toBe("home.example.zone");
  expect(z[0].summary).toBe("Edit zone home.example: add printer.home.example A");
  expect(z[0].text).toContain("nas IN A 192.0.2.10\n"); // the file as it was, kept
  expect(z[0].text).toMatch(/\nprinter\tIN\tA\t192\.0\.2\.45\n$/);
  expect(z[0].text).toContain("2026100702");
  await expect(page.locator("#pending")).toContainText("Edit zone home.example: add printer.home.example A");
  await expect(recs).toHaveCount(5);

  // Edit: the file written from its records.
  await page.getByRole("button", { name: "Edit nas A" }).click();
  await page.locator("#rvalue").fill("192.0.2.11");
  await page.locator("#rsave").click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  z = changePosts(state, "zone");
  expect(z).toHaveLength(2);
  expect(z[1].text).toMatch(/^\$ORIGIN home\.example\.\n/);
  expect(z[1].text).toContain("nas\t3600\tIN\tA\t192.0.2.11");
  expect(z[1].text).not.toContain("192.0.2.10");

  // Delete.
  await page.getByRole("button", { name: "Delete printer A" }).click();
  await page.locator("#rdelete").click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  z = changePosts(state, "zone");
  expect(z).toHaveLength(3);
  expect(z[2].summary).toBe("Edit zone home.example: remove printer.home.example A");
  expect(z[2].text).not.toContain("printer");

  // Advanced: an earlier version put back.
  await page.locator("#history-row summary").click();
  await page.locator("#history-row").getByRole("button", { name: "Put back" }).click();
  await expect(page.locator(".toast")).toContainText("is waiting to apply");
  z = changePosts(state, "zone");
  expect(z).toHaveLength(4);
  expect(z[3].text).not.toContain("nas IN A");

  // Undo: every change of the zone discarded.
  await page.locator("#undo").click();
  await expect(page.locator("#pending")).toHaveCount(0);
  expect(state.posts.filter(([k]) => k === "discard").length).toBeGreaterThan(0);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("A copied zone: its records read-only; one the nodes can't copy, with the fix", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("zones.html#zone=lab.example");
  await expect(page.locator(".muted").first()).toContainText(`Copied from your primary (${PRIMARY}) · 12 records · version 7`);
  await expect(page.getByRole("link", { name: /Edit at your primary/ })).toHaveCount(0);
  // Where to edit them, with the primary's address to copy.
  await expect(page.locator("#edit-at")).toContainText(`Edit these records on your primary DNS server,${PRIMARY}`);
  await expect(page.locator("#edit-at")).toContainText("They're read-only here.");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.locator("#edit-at").getByRole("button", { name: `Copy ${PRIMARY}` }).click();
  await expect(page.locator(".toast")).toContainText(`Copied ${PRIMARY}`);
  // The records, by name (the zone itself first) then type; no edit controls.
  await expect(page.locator(".recs tbody tr")).toHaveCount(5);
  await expect(page.locator(".recs tbody tr td:first-child")).toHaveText(["@", "@", "@", "nas", "printer"]);
  await expect(page.locator(".recs tbody tr td:nth-child(2)")).toHaveText(["NS", "SOA", "TXT", "A", "A"]);
  await expect(page.locator(".recs button, .recs input")).toHaveCount(0);

  // The primary refuses the controller: said plainly, with this computer's address (as the
  // primary saw it) to copy; the rcode small, after.
  state.refuse = true;
  await page.getByRole("button", { name: "Read again" }).click();
  await expect(page.locator("#records-error")).toHaveText(`Your primary DNS server (${PRIMARY}) refused to send this zone to the controller.`);
  await expect(page.locator("#records-error")).toHaveClass(/bad/);
  const why = page.locator("#records-error + p");
  await expect(why).toHaveText(`Your nodes have their copy; only this list is missing. To see the records here, allow zone transfers from this computer, as ${SOURCE}, on your primary.`);
  await why.getByRole("button", { name: `Copy ${SOURCE}` }).click();
  await expect(page.locator(".toast")).toContainText(`Copied ${SOURCE}`);
  await expect(page.locator("section p.small.muted")).toHaveText("rcode 5 (REFUSED)");
  await expect(page.locator("#records-error")).not.toContainText("rcode");
  await expect(page.locator(".recs")).toHaveCount(0);
  state.refuse = false;
  await expect(page.locator("#problem")).toHaveCount(0);

  // Stop copying it: asked, then both configs without it.
  await page.locator("#remove-zone").click();
  await page.locator("#confirm-remove").click();
  await expect(page.locator("#pending")).toBeVisible();
  const cs = changePosts(state, "config");
  expect(cs).toHaveLength(2);
  for (const c of cs) expect(JSON.parse(c.text).secondary.zones).toEqual(["stale.example"]);
  await expect(page.locator("#pending")).toContainText("this zone is removed with the next Apply");

  // An expired one: what to check on the primary; nothing is changed there from here.
  await page.goto("zones.html#zone=stale.example");
  await expect(page.locator("#problem")).toContainText("Your nodes can't copy this zone from your primary.");
  await expect(page.locator("#problem")).toContainText("Usually your primary doesn't allow zone transfers from their network.");
  await expect(page.locator("#problem")).toContainText("check that stale.example allows zone transfers from your nodes' networks (192.0.2.0/24)");
  await expect(page.locator(".recs")).toHaveCount(0);
  // Its records refused too: the nodes have no good copy, so it isn't said they do.
  await expect(page.locator("#records-error")).toHaveText(`Your primary DNS server (${PRIMARY}) refused to send this zone to the controller.`);
  await expect(page.locator("#records-error + p")).toContainText("The controller can't read the records either. To see the records here, allow zone transfers from this computer, as ");
  await expect(page.locator("#zones, .zpanel")).toHaveCount(0);
  await expect(page.locator("#allow")).toHaveCount(0);
  expect(state.posts.filter(([k]) => k === "job")).toEqual([]);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("A forwarded zone: its server changed as a pending change", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("zones.html#zone=corp.example");
  await expect(page.locator("main")).toContainText(`Queries for corp.example and every name under it go to ${FWD}`);
  await page.locator("#fwd-row summary").click();
  await page.locator("#fto").fill(FWD2);
  await page.locator("#fsave").click();
  await expect(page.locator("#pending")).toContainText(`Edit zone corp.example: forward to ${FWD2}`);
  const cs = changePosts(state, "config");
  expect(cs).toHaveLength(2);
  for (const c of cs) expect(JSON.parse(c.text).forward_zones).toEqual([{ zone: "corp.example", forwarder: FWD2 }]);
  await expect(page.locator(".muted").first()).toContainText(`Forwarded to ${FWD2}`);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Undo names the other changes it takes with it", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("zones.html");
  await page.getByRole("button", { name: "+ Add zone" }).click();
  await page.locator("#zq").fill("iot.example");
  await page.locator("#zqres").getByRole("button", { name: "Copy from your primary" }).click();
  await expect(page.locator('#zones .r[data-zone="iot.example"]')).toContainText("Waiting to apply");
  // A node's settings edited after: stacked on the zone's change to dns2's config.
  state.changes.push({ id: "c90", kind: "config", name: "dns2.json", summary: "dns2: its name", effect: "" });
  await page.goto("zones.html#zone=iot.example");
  await page.reload();
  await expect(page.locator("#pending")).toBeVisible();
  await page.locator("#undo").click();
  const dlg = page.getByRole("dialog");
  await expect(dlg).toContainText("Undo the changes to iot.example?");
  await expect(dlg).toContainText("dns2: its name");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await expect(dlg).toHaveCount(0);
  expect(state.posts.filter(([k]) => k === "discard")).toHaveLength(0);
  await page.locator("#undo").click();
  await page.locator("#confirm-undo").click();
  await expect(page).not.toHaveURL(/#zone=/);
  expect(state.posts.filter(([k]) => k === "discard").length).toBeGreaterThan(0);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      const dir = path.join(shots, "zones");
      const shot = async (name, full = true) => {
        // A full page from the top, so the sticky bar is drawn once, where it belongs.
        if (full) await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: path.join(dir, `${name}-${width}-${scheme}.png`), fullPage: full });
      };
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      await login(page);
      const state = await fake(page);
      await page.goto("zones.html");
      await expect(page.locator("#zones .r:not(.headr)")).toHaveCount(4);
      await shot("zones");
      await page.locator("#primary-row summary").click();
      await expect(page.locator("#pa")).toBeVisible();
      await shot("zone-primary");
      await page.locator("#primary-row summary").click();
      await page.getByRole("button", { name: "+ Add zone" }).click();
      await page.locator("#zq").fill("iot.example");
      await expect(page.locator("#zqres")).toContainText("Found on your primary");
      await shot("add-zone");
      await page.locator("#zq").fill("new.example");
      await expect(page.locator("#zqres")).toContainText("Host it here");
      await page.locator("#zqres").getByText("Set it up myself").click();
      await shot("add-zone-manual");
      await page.locator("#zq").press("Escape");
      await page.goto("zones.html#zone=home.example");
      await expect(page.locator(".recs tbody tr")).toHaveCount(4);
      await shot("hosted");
      await page.locator("#add-record").click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await shot("add-record", false);
      await page.keyboard.press("Escape");
      await page.locator("#zfile-row summary").click();
      await shot("hosted-file");
      await page.goto("zones.html#zone=lab.example");
      await expect(page.locator(".recs tbody tr")).toHaveCount(5);
      await shot("copied");
      state.refuse = true;
      await page.getByRole("button", { name: "Read again" }).click();
      await expect(page.locator("#records-error")).toBeVisible();
      await shot("copied-refused");
      state.refuse = false;
      await page.goto("zones.html#zone=stale.example");
      await expect(page.locator("#problem")).toBeVisible();
      await expect(page.locator("#records-error")).toBeVisible();
      await shot("copied-problem");
      await page.goto("zones.html#zone=corp.example");
      await page.locator("#fwd-row summary").click();
      await shot("forwarded");
    });
  }
}
