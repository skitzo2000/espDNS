// The Blocking page (blocklists.html, blocking.js) against a fake controller: serve.sh's
// controller logs in and serves the page and the list catalogue (its real
// /api/blocking/setup, with no list), and the rest is answered here (page.route), with
// documentation addresses (RFC 5737) and names, so nothing goes on the network.
// E2E_SHOTS=<dir> also takes the page, + Add blocklist, the picker and the changes at 375
// and 1366 px, light and dark, into <dir>/blocking.
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login } = require("./session");

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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", CLIENT = "192.0.2.160", GATEWAY = "192.0.2.1";
const URLFEED = "https://lists.example.com/hosts.txt";

function node(addr, id, name) {
  return {
    id, addr, hostname: `${name}.local`, source: "settings", online: true, last_seen: new Date().toISOString(), polled: new Date().toISOString(), qps: 0.4,
    status: { board: "p4-ip101", version: "0.0.4", uptime_s: 9660, net: { kind: "ethernet", hostname: `${name}.local` }, health: { state: "healthy", answering: true, reasons: [] }, config: { name, source: "node" } },
  };
}

// fake answers the page's APIs from a state the edits change; state.posts is what was sent.
async function fake(page, opts = {}) {
  const iso = s => new Date(Date.now() + s * 1000).toISOString();
  const state = { posts: [], changes: [], apply: opts.apply || null, catalog: null, defined: opts.defined !== false, n: 0,
    lists: [], allowed: [], blocked: [],
    // The schedule (refresh.go): every day, checked 2 hours ago, unchanged.
    refresh: { every: "24h", choices: ["off", "6h", "12h", "24h", "168h"], list: "main", downloads: true, checking: false, sending: false,
      checked: iso(-2 * 3600), updated: iso(-20 * 3600), next: iso(22 * 3600), outcome: "unchanged", message: "", over: [], ...opts.refresh } };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/nodes$/, r => json(r, [node(DNS2, "aa:bb:cc:00:00:02", "dns2"), node(DNS3, "aa:bb:cc:00:00:03", "dns3")]));
  await page.route(/\/api\/dashboard$/, r => json(r, { window_s: 3600, bin_s: 60, bins: 60, nodes: [], fleet: { now: { qps: 0.4 }, hour: { qps: 0.4, blocked: 212, blocked_share: 0.009 } } }));
  await page.route(/\/api\/blocking\/setup$/, async r => {
    if (!state.catalog) {
      const real = await (await r.fetch({ method: "GET" })).json();
      state.catalog = real;
      const c = id => real.catalog.find(x => x.id === id);
      state.lists = [["hagezi-pro", 227572], ["oisd-big", 244437], ["stevenblack", 74760], ["hagezi-tif", 2370238]].map(([id, n]) => {
        const x = c(id);
        return { source: x.source, shown: x.source, title: x.name, catalog: x.id, what: x.what, group: x.group, by: x.by, on: true, allow: false, waiting: false, domains: n, counted: true,
          download: true, updated: iso(-20 * 3600), checked: iso(-2 * 3600) };
      });
    }
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON();
      state.posts.push(["setup", b]);
      const add = (summary, kind) => { const ch = { id: `c${++state.n}`, kind, summary, effect: "Blocking: compiled again, then sent to the nodes, live (no restart)" }; state.changes.push(ch); return ch; };
      let ch;
      if (b.op === "add") {
        for (const s of b.sources) {
          const x = state.catalog.catalog.find(c => c.source === s);
          state.lists.push({ source: s, shown: s, title: x ? x.name : new URL(s.replace(/^[a-z]+:/, "")).hostname, catalog: x ? x.id : "", what: x ? x.what : "",
            on: true, allow: false, waiting: true, domains: 0, counted: false });
        }
        state.defined = true;
        ch = add(b.sources.length === 1 ? `Add blocklist ${state.lists[state.lists.length - 1].title}` : `Add ${b.sources.length} blocklists`, "lists");
      } else if (b.op === "unuse" || b.op === "use") {
        const l = state.lists.find(x => x.source === b.source);
        l.on = b.op === "use";
        l.waiting = true;
        ch = add(`${l.on ? "Use" : "Stop using"} blocklist ${l.title}`, "lists");
      } else if (b.op === "remove") {
        const l = state.lists.find(x => x.source === b.source);
        state.lists = state.lists.filter(x => x !== l);
        ch = add(`Remove blocklist ${l.title}`, "lists");
      } else {
        const site = b.site.trim().toLowerCase();
        const arr = b.op.includes("allow") ? state.allowed : state.blocked;
        if (b.op === "allow" || b.op === "block") arr.push({ site, waiting: true });
        else arr.splice(arr.findIndex(x => x.site === site), 1);
        ch = add({ allow: "Always allow ", block: "Always block ", unallow: "Stop always allowing ", unblock: "Stop always blocking " }[b.op] + site, "source");
      }
      return json(r, { changes: [ch] }, 201);
    }
    return json(r, { ...state.catalog, list: "main", defined: state.defined, hash: "h", others: [], pending: state.changes.length > 0, refresh: state.refresh,
      report: state.defined ? { time: new Date(Date.now() - 6 * 3600e3).toISOString(), blocked: 2587385, allowed: 0, bytes: 9000000 } : null,
      lists: state.defined ? state.lists : [], allowed: state.allowed, blocked: state.blocked,
      nodes: opts.defined !== false ? [{ host: DNS2, online: true, state: opts.paused ? "paused" : "on", paused_s: opts.paused ? 1800 : 0, current: true, entries: 2587385, room: 3900000 },
        { host: DNS3, online: true, state: "off", room: -1 }] : [] });
  });
  await page.route(/\/api\/blocking\/check\?/, r => {
    const name = new URL(r.request().url()).searchParams.get("name");
    if (/roblox/.test(name)) {
      return json(r, { check: { name, result: "blocked", by: "list", entries: [{ name: "roblox.com", suffix: true, source: 0 }], specs: [], list: "main", indexed: true,
        compiled: new Date(Date.now() - 6 * 3600e3).toISOString() }, sources: [{ spec: "wildcard:https://lists.example.com/pro.txt", title: "HaGeZi Pro" }],
      nodes: [{ host: DNS2, blocks: true }, { host: DNS3, blocks: false, why: "off" }] });
    }
    return json(r, { check: { name, result: "not blocked", by: "", entries: [], specs: [], list: "main", indexed: true }, sources: [], nodes: [] });
  });
  await page.route(/\/api\/querylog\?/, r => json(r, { entries: [{ id: 9, node: "aa:bb:cc:00:00:02", at: new Date(Date.now() - 120e3).toISOString(), client: CLIENT,
    qname: "tracing.roblox.com", qtype: "A", result: "blocked", rule: "list" }] }));
  // Who asked: the gateway (it forwards for the devices behind it), a device named from the
  // zones, one whose node keeps no address, and 2 more; for any other site, no one.
  await page.route(/\/api\/querylog\/askers\?/, r => {
    const name = new URL(r.request().url()).searchParams.get("name");
    const ago = s => new Date(Date.now() - s * 1000).toISOString();
    const base = { name, held: 8000, cap: 20000, oldest: ago(3 * 3600), queries: 0, more: 0, askers: [],
      nodes: [{ key: "aa:bb:cc:00:00:02", name: "dns2", addr: DNS2, enabled: true, state: "ok", capacity: 512 },
        { key: "aa:bb:cc:00:00:03", name: "dns3", addr: DNS3, enabled: true, state: "ok", capacity: 512 }] };
    if (!/roblox/.test(name)) return json(r, base);
    return json(r, { ...base, queries: 20, more: 2, askers: [
      { client: GATEWAY, queries: 14, blocked: 14, last: ago(120), nodes: ["aa:bb:cc:00:00:02", "aa:bb:cc:00:00:03"], gateway: true },
      { client: CLIENT, name: "kids-tablet.home.example", queries: 3, blocked: 3, last: ago(12 * 60), nodes: ["aa:bb:cc:00:00:02"] },
      { client: null, queries: 1, blocked: 1, last: ago(40 * 60), nodes: ["aa:bb:cc:00:00:03"] },
    ] });
  });
  await page.route(/\/api\/blocking\/refresh\/schedule$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["schedule", b]);
    state.refresh.every = b.every;
    if (b.every === "off") delete state.refresh.next;
    return json(r, { every: b.every });
  });
  await page.route(/\/api\/blocking\/refresh$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["refresh", b]);
    if (b.accept_change) {
      state.refresh.sending = true;
      return json(r, { job: { id: "j9", kind: "refresh", state: "queued" } }, 202);
    }
    state.refresh.checking = true;
    return json(r, { checking: true }, 202);
  });
  await page.route(/\/api\/changes$/, r => json(r, { changes: state.changes, restarts: [], apply: state.apply }));
  await page.route(/\/api\/changes\/[^/]+\/discard$/, r => {
    const id = decodeURIComponent(r.request().url().split("/").slice(-2)[0]);
    state.posts.push(["discard", id]);
    state.changes = state.changes.filter(c => c.id !== id);
    if (!state.changes.length) state.apply = null;
    return json(r, { discarded: [id] });
  });
  await page.route(/\/api\/changes\/file\?/, r => json(r, { kind: "lists", name: "lists.json", hash: "h", exists: true, pending: [], size: 20, text: '{"lists": []}\n' }));
  await page.route(/\/api\/blocking$/, r => json(r, { sources: [{ name: "overrides.txt" }], lists: [{ name: "main", command: "espdns blocklist -list wildcard:https://... -out /data/lists/main.bin" }] }));
  await page.route(/\/api\/jobs$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["job", b]);
    return json(r, { id: `j${state.posts.length}`, kind: b.kind, who: "admin", state: "queued", created: new Date().toISOString() }, 201);
  });
  await page.route(/\/api\/jobs\/j\d+$/, r => json(r, { id: "j", kind: "pause", state: "done" }));
  await page.route(/\/api\/jobs\/j\d+\/events/, r => r.fulfill({ status: 404, contentType: "application/json", body: '{"error":"no such job"}' }));
  return state;
}

test("Blocking: its state, the check of the last site blocked, the rules and the lists", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("blocklists.html");
  await expect(page.locator("#title")).toHaveText("Blocking is on");
  const nav = page.locator('nav[aria-label="Main"] a');
  await expect(nav.nth(2)).toHaveText("Blocking");
  await expect(nav.nth(2)).toHaveAttribute("aria-current", "page");
  const facts = page.locator("#facts");
  await expect(facts).toContainText("2,587,385 domains from 4 lists");
  await expect(facts).toContainText("0.9% of queries blocked in the last hour (212)");
  await expect(facts).toContainText("On dns2 · off on dns3 (its settings)");
  await expect(page.locator("#pause")).toContainText("Pause blocking on every node for a while");

  // Check a site starts with the last site blocked.
  await expect(page.locator("#checkq")).toHaveValue("tracing.roblox.com");
  const v = page.locator("#verdict .verdict");
  await expect(v).toHaveClass(/blocked/);
  await expect(v).toContainText("tracing.roblox.com is blocked");
  await expect(v).toContainText("By HaGeZi Pro (as roblox.com, with everything under it).");
  await expect(v).toContainText("Blocked on dns2; not on dns3 (blocking off in its settings).");

  // Who asked: every device, the most recent first, each to its queries; the gateway said
  // to be one; the rest in the query log by the name.
  const who = v.locator(".askers");
  await expect(who.locator("li")).toHaveCount(3);
  const [gw, kid, hidden] = [who.locator("li").nth(0), who.locator("li").nth(1), who.locator("li").nth(2)];
  await expect(gw.locator("a.dev")).toHaveText(GATEWAY);
  await expect(gw.locator("a.dev")).toHaveAttribute("href", `querylog.html?client=${GATEWAY}`);
  await expect(gw).toContainText("14 times · 2 min ago · through dns2 and dns3");
  await expect(gw.locator(".tag")).toHaveText("your gateway");
  await expect(gw).toContainText("It asks for the devices behind it, so they aren't shown. To see each one, have that network's DHCP hand out your nodes as DNS servers (Copy DNS servers on Nodes).");
  await expect(gw.getByRole("link", { name: "Copy DNS servers" })).toHaveAttribute("href", "./");
  await expect(kid.locator("a.dev")).toHaveText("kids-tablet.home.example");
  await expect(kid.locator("a.dev")).toHaveAttribute("href", `querylog.html?client=${CLIENT}`);
  await expect(kid).toContainText(CLIENT);
  await expect(kid).toContainText("3 times · 12 min ago · through dns2");
  await expect(kid.locator(".tag")).toHaveCount(0);
  await expect(hidden).toContainText("A device (its node doesn't keep addresses)");
  await expect(hidden).toContainText("once · 40 min ago · through dns3");
  await expect(hidden.locator("a")).toHaveCount(0);
  await expect(who.getByRole("link", { name: "every query for tracing.roblox.com" })).toHaveAttribute("href", "querylog.html?name=tracing.roblox.com");
  await expect(who.getByRole("link", { name: "and 2 more" })).toHaveAttribute("href", "querylog.html?name=tracing.roblox.com");

  await expect(page.locator("#rules details")).toHaveCount(2);
  await expect(page.locator("#rules summary").first()).toContainText("Always allowed");
  await expect(page.locator("#rules summary").first()).toContainText("None");
  const rows = page.locator("#lists details");
  await expect(rows).toHaveCount(4);
  await expect(rows.first().locator("summary")).toContainText("HaGeZi Pro");
  await expect(rows.first().locator("summary")).toContainText("227,572 domains");
  await expect(page.locator("#lists-hint")).toContainText("dns2 has room for about 3.9 M more domains.");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Allow from Check a site, and the rules: each a pending change", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("blocklists.html");
  await page.getByRole("button", { name: "Allow tracing.roblox.com" }).click();
  await expect(page.locator("#toast")).toContainText("Always allow tracing.roblox.com");
  await expect(page.locator(".changes .chip-btn")).toHaveText("1 change to apply");
  expect(state.posts.find(p => p[0] === "setup")[1]).toEqual({ op: "allow", site: "tracing.roblox.com" });
  const allowed = page.locator('#rules details[data-key="allow"]');
  await expect(allowed).toHaveAttribute("open", "");
  await expect(allowed).toContainText("tracing.roblox.com");
  await expect(allowed).toContainText("waiting to apply");

  // Block a site by hand, then take it out again.
  const blocked = page.locator('#rules details[data-key="block"]');
  await blocked.locator("summary").click();
  await page.locator("#addblock").fill("ads.example.com");
  await page.locator("#addblock").press("Enter");
  await expect(blocked).toContainText("ads.example.com");
  await blocked.getByRole("button", { name: "Remove ads.example.com" }).click();
  await expect(blocked).not.toContainText("ads.example.com");
  expect(state.posts.filter(p => p[0] === "setup").map(p => p[1].op)).toEqual(["allow", "block", "unblock"]);
  // Check a site typed: not blocked, with Block.
  await page.locator("#checkq").fill("docs.example.org");
  await page.locator("#checkq").press("Enter");
  await expect(page.locator("#verdict")).toContainText("docs.example.org is not blocked");
  await expect(page.locator("#verdict")).toContainText("Not in the nodes' recent query logs (the controller keeps their last 20,000 queries; the oldest is from 3 h ago).");
  await expect(page.locator("#verdict .askers")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Block docs.example.org" })).toBeVisible();
  await expect(page).toHaveURL(/#check=docs\.example\.org$/);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("+ Add blocklist: the catalogue under the box, a list by its address, and the picker", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("blocklists.html");
  await page.getByRole("button", { name: "+ Add blocklist" }).click();
  const box = page.locator("#lq"), list = page.locator("#lqlist");
  await expect(box).toBeFocused();
  await box.fill("oisd");
  await expect(list.locator(".opt")).toHaveCount(3); // Small, Big, NSFW
  await expect(list.locator(".opt").filter({ hasText: "OISD Big" })).toContainText("Added");
  await list.getByRole("button", { name: "Add OISD Small" }).click();
  await expect(page.locator("#toast")).toContainText("Added OISD Small");
  await expect(list.locator(".opt").filter({ hasText: "OISD Small" })).toContainText("Added");
  const added = state.posts.find(p => p[0] === "setup")[1];
  expect(added.op).toBe("add");
  expect(added.sources).toEqual([state.catalog.catalog.find(c => c.id === "oisd-small").source]);
  await expect(page.locator("#lists details")).toHaveCount(5);
  await expect(page.locator("#lists details").last()).toContainText("after apply");

  // A web address: read as the kind it looks like.
  await box.fill(URLFEED);
  await expect(list.locator(".opt.url")).toContainText("Add the list at this address");
  await expect(list.locator(".opt.url select")).toHaveValue("hosts");
  await list.locator(".opt.url").getByRole("button", { name: "Add" }).click();
  await expect.poll(() => state.posts.filter(p => p[0] === "setup").length).toBe(2);
  expect(state.posts.filter(p => p[0] === "setup")[1][1].sources).toEqual([`hosts:${URLFEED}`]);
  await expect(box).toHaveValue("");

  // A click elsewhere with something typed hides the list, and it comes back on focus.
  await box.fill("hagezi");
  await expect(list).toBeVisible();
  await page.locator("#check-h").click();
  await expect(list).toBeHidden();
  await expect(box).toHaveAttribute("aria-expanded", "false");
  await box.click();
  await expect(list).toBeVisible();

  // The picker: by purpose, a checkbox each.
  await box.fill("");
  await list.getByRole("button", { name: "Browse every list by what it blocks" }).click();
  const d = page.getByRole("dialog", { name: "Add blocklists" });
  await expect(d).toBeVisible();
  await d.getByRole("button", { name: "Gambling" }).click();
  await expect(d.locator(".cat")).toHaveCount(2);
  for (const c of await d.locator(".cat input").all()) await c.check();
  await d.getByRole("button", { name: "Social media" }).click();
  await d.locator(".cat").filter({ hasText: "Block List Project: TikTok" }).locator("input").check();
  await d.getByRole("button", { name: "Add 3 lists" }).click();
  await expect(d).toHaveCount(0);
  const picked = state.posts.filter(p => p[0] === "setup")[2][1];
  expect(picked.sources.length).toBe(3);
  await expect(page.locator(".changes .chip-btn")).toHaveText("3 changes to apply");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a list turned off and removed; pause on every node; Advanced", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("blocklists.html");
  const row = page.locator("#lists details").filter({ hasText: "OISD Big" });
  await row.locator("summary").click();
  await expect(row).toContainText("oisd.nl");
  await row.getByRole("switch", { name: "Use OISD Big" }).click();
  await expect(row.locator("summary")).toContainText("Not in use");
  await expect(row).toHaveAttribute("open", ""); // kept open across the redraw
  await row.getByRole("button", { name: "Remove list" }).click();
  await expect(page.locator("#lists details")).toHaveCount(3);
  expect(state.posts.filter(p => p[0] === "setup").map(p => p[1].op)).toEqual(["unuse", "remove"]);

  await page.locator("#pausefor").selectOption({ label: "1 hour" });
  await page.getByRole("button", { name: "Pause", exact: true }).click();
  await expect(page.locator("#toast")).toContainText("Blocking paused until");
  const jobs = state.posts.filter(p => p[0] === "job").map(p => p[1]);
  expect(jobs).toEqual([{ kind: "pause", params: { node: DNS2, seconds: 3600 } }]); // not dns3: off in its settings

  const adv = page.locator('#advanced details[data-key="advanced"]');
  await adv.locator("summary").click();
  await expect(adv).toContainText("Each node");
  await expect(adv.locator("#adv-nodes .nrow")).toHaveCount(2);
  await expect(adv.locator("textarea").first()).toHaveValue('{"lists": []}\n');
  await expect(adv.locator("#cmd")).toContainText("espdns blocklist");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("paused: the state says until when, with Resume", async ({ page }) => {
  await login(page);
  const state = await fake(page, { paused: true });
  await page.goto("blocklists.html");
  await expect(page.locator("#title")).toHaveText("Blocking is paused");
  await expect(page.locator("#pause")).toContainText("Nothing is blocked until");
  await page.getByRole("button", { name: "Resume now" }).click();
  await expect(page.locator("#toast")).toContainText("Blocking resumed");
  expect(state.posts.filter(p => p[0] === "job").map(p => p[1])).toEqual([{ kind: "pause", params: { node: DNS2, seconds: 0 } }]);
});

test("off: + Add blocklist is the one thing to do", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, { defined: false });
  await page.goto("blocklists.html");
  await expect(page.locator("#off")).toBeVisible();
  await expect(page.locator("#off h2")).toHaveText("Blocking is off");
  await expect(page.locator("#on")).toBeHidden();
  const add = page.locator("#off").getByRole("button", { name: "+ Add blocklist" });
  await expect(add).toHaveClass(/primary/);
  await add.click();
  await page.locator("#off #lq").fill("hagezi pro");
  await page.locator("#off #lqlist").getByRole("button", { name: "Add HaGeZi Pro", exact: true }).click();
  await expect(page.locator("#title")).toHaveText("Blocking isn't on yet");
  await expect(page.locator("#on")).toBeVisible();
  expect(state.posts[0][1].op).toBe("add");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("the size-change check asks: apply anyway, or keep what the nodes have", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const q = { kind: "size-change", list: "main", text: "main changed more than its limit (blocked 2587385 → 1552431 (-40%))", params: { accept_change: ["main"], soak_s: 60 } };
  const state = await fake(page, { apply: { id: "j0", kind: "apply", state: "stopped", progress: { questions: [q] } } });
  state.changes.push({ id: "c8", kind: "source", name: "overrides-allow.txt", summary: "Always allow docs.example.org", effect: "" });
  state.changes.push({ id: "c9", kind: "lists", name: "lists.json", summary: "Add blocklist OISD Small", effect: "" });
  await page.goto("blocklists.html");
  const ask = page.locator("#ask");
  await expect(ask).toContainText("Your blocklists changed size more than usual");
  await expect(ask).toContainText("Your blocklists: domains blocked: 2,587,385 → 1,552,431 (-40%).");
  await ask.getByRole("button", { name: "Apply, taking this change" }).click();
  await expect.poll(() => state.posts.filter(p => p[0] === "job").length).toBe(1);
  expect(state.posts.find(p => p[0] === "job")[1]).toEqual({ kind: "apply", params: q.params });

  await page.reload();
  await page.locator("#ask").getByRole("button", { name: "Keep what the nodes have" }).click();
  await expect(page.locator("#toast")).toContainText("the nodes keep the lists they have");
  // Only the change behind the question: the always-allowed site waits on.
  expect(state.posts.filter(p => p[0] === "discard").map(p => p[1])).toEqual(["c9"]);
  await expect(page.locator("#ask")).toBeHidden();
  expect(await violations()).toEqual([]);
});

test("the refresh schedule: how often, when checked, Refresh now", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("blocklists.html");
  const box = page.locator("#refresh");
  await expect(box).toBeVisible();
  await expect(box.locator("#refresh-every")).toHaveValue("24h");
  await expect(box.locator("#refresh-every option")).toHaveText(["Never", "Every 6 hours", "Every 12 hours", "Every day", "Every week"]);
  await expect(box.locator("#refresh-when")).toContainText("Checked 2 h ago · next ");
  // Each downloaded list: when its version came, and when it was checked.
  const row = page.locator("#lists details").first();
  await row.locator("summary").click();
  await expect(row.locator("dl")).toContainText("Updated20 h ago");
  await expect(row.locator("dl")).toContainText("Checked2 h ago");

  await box.locator("#refresh-every").selectOption("168h");
  await expect(page.locator("#toast")).toContainText("The lists are checked every week");
  expect(state.posts.find(p => p[0] === "schedule")[1]).toEqual({ every: "168h" });
  await expect(box.locator("#refresh-every")).toHaveValue("168h");

  await box.getByRole("button", { name: "Refresh now" }).click();
  await expect(page.locator("#toast")).toContainText("Checking the lists for new versions");
  expect(state.posts.find(p => p[0] === "refresh")[1]).toEqual({});
  await expect(box.locator("#refresh-now")).toHaveText("Checking…");
  await expect(box.locator("#refresh-now")).toBeDisabled();
  await expect(box.locator("#refresh-when")).toHaveText("Checking now…");

  state.refresh.checking = false; // the check ended
  await box.locator("#refresh-every").selectOption("off");
  await expect(box.locator("#refresh-every")).toHaveValue("off");
  await expect(box.locator("#refresh-when")).toContainText("only when you refresh or apply a change");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a new version refused by the size-change check: said in the bad style, with what to do", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, { refresh: { outcome: "refused", message: "changed by more than its size-change limit: blocked 2587385 → 12040 (-99.5%)",
    over: ["blocked 2587385 → 12040 (-99.5%)"] } });
  await page.goto("blocklists.html");
  const refused = page.locator("#refresh-refused");
  await expect(refused.locator("p.bad")).toHaveText("Not updated: the new version changed more than its limit (domains blocked: 2,587,385 → 12,040 (-99.5%)).");
  await expect(refused).toContainText("The nodes keep the lists they have, and the next check tries a newer version. If the change is expected, send it anyway.");
  await refused.getByRole("button", { name: "Send it anyway" }).click();
  await expect(page.locator("#toast")).toContainText("Sending the new version, taking the change in size");
  expect(state.posts.find(p => p[0] === "refresh")[1]).toEqual({ list: "main", accept_change: true });
  await expect(page.locator("#refresh-when")).toHaveText("Sending a new version to the nodes…");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a check that failed, and one waiting for the changes to apply", async ({ page }) => {
  await login(page);
  const state = await fake(page, { refresh: { outcome: "failed", message: "https://lists.example.com/pro.txt: HTTP 503 Service Unavailable" } });
  await page.goto("blocklists.html");
  await expect(page.locator("#refresh-failed")).toHaveClass(/bad/);
  await expect(page.locator("#refresh-failed")).toHaveText("The last check failed: https://lists.example.com/pro.txt: HTTP 503 Service Unavailable. It's tried again within an hour; the nodes keep the lists they have.");
  state.refresh.outcome = "waiting";
  await page.reload();
  await expect(page.locator("#refresh-waiting")).toHaveText("Waiting for your changes: applying them fetches the lists.");
  await expect(page.locator("#refresh-failed")).toHaveCount(0);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
// full is a full-page shot from the top, so the sticky bar is drawn where it belongs.
async function full(page, file) {
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: file, fullPage: true });
}
const noToast = page => expect(page.locator("#toast")).toBeHidden({ timeout: 10000 }).catch(() => {});
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      const dir = path.join(shots, "blocking");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      await login(page);
      await fake(page);
      await page.goto("blocklists.html");
      await expect(page.locator("#verdict .verdict")).toBeVisible();
      await expect(page.locator("#facts")).toContainText("of queries blocked");
      await full(page, path.join(dir, `blocking-${width}-${scheme}.png`));
      await expect(page.locator("#verdict .askers li")).toHaveCount(3);
      await page.locator("#verdict").screenshot({ path: path.join(dir, `check-${width}-${scheme}.png`) });
      await page.locator("#lists details").first().locator("summary").click();
      await page.locator('#rules details[data-key="allow"] summary').click();
      await full(page, path.join(dir, `blocking-open-${width}-${scheme}.png`));
      await page.getByRole("button", { name: "+ Add blocklist" }).click();
      await page.locator("#lq").fill("hagezi");
      await page.screenshot({ path: path.join(dir, `add-blocklist-${width}-${scheme}.png`) });
      await page.locator("#lqlist").getByRole("button", { name: "Add HaGeZi Light" }).click();
      await expect(page.locator(".changes .chip-btn")).toHaveText("1 change to apply");
      await page.locator("#lq").fill("");
      await page.locator("#lqlist").getByRole("button", { name: "Browse every list by what it blocks" }).click();
      await expect(page.getByRole("dialog", { name: "Add blocklists" })).toBeVisible();
      await noToast(page);
      await page.screenshot({ path: path.join(dir, `picker-${width}-${scheme}.png`) });
      await page.keyboard.press("Escape");
      await page.locator(".changes .chip-btn").click();
      await expect(page.getByRole("dialog", { name: "Changes to apply" })).toBeVisible();
      await page.screenshot({ path: path.join(dir, `changes-${width}-${scheme}.png`) });
      await page.keyboard.press("Escape");
      await page.locator('#advanced details summary').click();
      await noToast(page);
      await full(page, path.join(dir, `advanced-${width}-${scheme}.png`));
    });
    test(`screenshots: the refresh refused, ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      await login(page);
      await fake(page, { refresh: { outcome: "refused", message: "changed by more than its size-change limit", over: ["blocked 2587385 → 12040 (-99.5%)"] } });
      await page.goto("blocklists.html");
      await expect(page.locator("#refresh-refused")).toBeVisible();
      await page.locator("#lists details").first().locator("summary").click();
      await page.locator("section[aria-labelledby=lists-h]").screenshot({ path: path.join(shots, "blocking", `refresh-refused-${width}-${scheme}.png`) });
    });
  }
}

test("off: screenshots", async ({ page }) => {
  test.skip(!shots, "E2E_SHOTS not set");
  for (const scheme of ["light", "dark"]) {
    for (const width of [375, 1366]) {
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      if (width === 375 && scheme === "light") { await login(page); await fake(page, { defined: false }); }
      await page.goto("blocklists.html");
      await expect(page.locator("#off")).toBeVisible();
      await full(page, path.join(shots, "blocking", `off-${width}-${scheme}.png`));
    }
  }
});
