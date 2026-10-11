// The top bar's search (shell.js, searchBox), on every page built on the shell: the box asks
// GET /api/search, answered here (page.route) with documentation addresses (RFC 5737) and
// example names, so nothing goes on the network. E2E_SHOTS=<dir> also takes the results at
// 375 and 1366 px, light and dark, into <dir>/search.
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

const DNS2 = "192.0.2.53", LAPTOP = "198.51.100.23";
const last = new Date().toISOString();
// What /api/search answers for "home", one result of every type (internal/search's shapes).
const RESULTS = [
  { type: "node", title: "dns2", text: `p4-ip101 at ${DNS2}, healthy`, page: "nodes", key: "aa:bb:cc:00:00:02", link: "#node-aa:bb:cc:00:00:02", match: "name", state: "healthy" },
  { type: "zone", title: "home.example", text: "hosted here: 12 records", page: "zones", key: "home.example", link: "#zone-home.example", match: "name", state: "ok" },
  { type: "record", title: "printer.home.example", text: "A 192.0.2.80", page: "zones", key: "printer.home.example", link: "#zone-home.example", match: "within", zone: "home.example", pending: true },
  { type: "device", title: "laptop.home.example", text: `${LAPTOP}: asked 40 times in the query log the controller holds`, page: "query-log", key: LAPTOP,
    link: `#query-log?client=${LAPTOP}`, match: "name", queries: 40, last },
  { type: "site", title: "homeassistant.example", text: "asked twice in the query log the controller holds", page: "query-log", key: "homeassistant.example",
    link: "#query-log?name=homeassistant.example", match: "name", queries: 2, last },
  { type: "site", title: "home.example.net", text: "asked once in the query log the controller holds", page: "query-log", key: "home.example.net",
    link: "#query-log?name=home.example.net", match: "name", queries: 1, last },
  { type: "rule", title: "homeads.example", text: "blocked, with its subdomains", page: "blocking", key: "homeads.example", link: "#blocking?site=homeads.example", match: "name", state: "block" },
  { type: "list", title: "home-extra", text: "a blocklist from 1 source", page: "blocking", key: "home-extra", link: "#blocking?list=home-extra", match: "name" },
  { type: "change", title: "Host home.example", text: "pending: changes home.example", page: "changes", key: "c1", link: "#changes?change=c1", match: "summary", pending: true },
];
const ANSWER = { query: "home", results: RESULTS, matched: { node: 1, zone: 1, record: 1, device: 1, site: 7, rule: 1, list: 1, change: 1 }, more: true, partial: false,
  searched: ["nodes", "zones", "records", "devices", "sites", "check", "rules", "lists", "changes"], problems: [] };

// fake answers /api/search with answer(q) (an object, or [status, body]) and /api/changes
// with one change; state.asked is every query asked.
async function fake(page, answer = () => ANSWER) {
  const state = { asked: [] };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/search\?/, r => {
    const q = new URL(r.request().url()).searchParams.get("q");
    state.asked.push(q);
    const a = answer(q);
    return Array.isArray(a) ? json(r, a[1], a[0]) : json(r, a);
  });
  await page.route(/\/api\/changes$/, r => json(r, { changes: [{ id: "c1", kind: "zone", name: "home.example", summary: "Host home.example", effect: "Zones: sent to the nodes, live (no restart)" }],
    restarts: [], apply: null }));
  return state;
}

const box = page => page.locator("#q");
const res = page => page.locator("#q-res");
const opts = page => page.locator("#q-res [role=option]");

test("search: the results render by type, with where each goes", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await expect(box(page)).toHaveAttribute("placeholder", "Search a site, device or node");
  await expect(res(page)).toBeHidden();
  await box(page).fill("home");
  await expect(opts(page)).toHaveCount(RESULTS.length);
  await expect(page.locator("#q-res .search-gh")).toHaveText(["Nodes", "Zones", "Records", "Devices", "Sites asked2 of 7", "Allowed and blocked sites", "Blocklists", "Changes to apply"]);
  await expect(box(page)).toHaveAttribute("aria-expanded", "true");
  const hrefs = await opts(page).evaluateAll(os => os.map(o => o.getAttribute("href")));
  expect(hrefs).toEqual([
    `node.html#${encodeURIComponent("aa:bb:cc:00:00:02")}`,
    "zones.html#zone=home.example",
    "zones.html#zone=home.example",
    `querylog.html?client=${LAPTOP}`,
    "querylog.html?name=homeassistant.example",
    "querylog.html?name=home.example.net",
    "blocklists.html#check=homeads.example",
    "blocklists.html#list=home-extra",
    null, // the changes panel
  ]);
  await expect(opts(page).nth(2)).toContainText("waiting to apply");
  await expect(opts(page).nth(8)).not.toContainText("waiting to apply");
  await expect(opts(page).nth(0)).toContainText(`p4-ip101 at ${DNS2}, healthy`);
  await expect(page.locator("#q-res .search-foot")).toContainText("Only the best matches are shown");
  expect(state.asked).toEqual(["home"]); // typed at once: one search, debounced
  expect(await violations()).toEqual([]);
});

test("search: / focuses it, arrows move, Enter goes to the result", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  await page.locator("main").click({ position: { x: 5, y: 5 } });
  await page.keyboard.press("/");
  await expect(box(page)).toBeFocused();
  await page.keyboard.type("home");
  await expect(opts(page)).toHaveCount(RESULTS.length);
  await page.keyboard.press("ArrowDown");
  await expect(box(page)).toHaveAttribute("aria-activedescendant", "q-opt-0");
  await expect(opts(page).nth(0)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("ArrowUp"); // wraps to the last
  await expect(box(page)).toHaveAttribute("aria-activedescendant", `q-opt-${RESULTS.length - 1}`);
  await page.keyboard.press("ArrowDown"); // and back to the first
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await expect(opts(page).nth(3)).toHaveClass(/\bon\b/);
  await page.keyboard.press("Enter");
  // The query log reads ?client= and keeps it as ?device=: that device alone.
  await expect(page).toHaveURL(new RegExp(`/querylog\\.html\\?(client|device)=${LAPTOP.replace(/\./g, "\\.")}$`));
  await expect(page.getByRole("button", { name: /^Device .*: show every device$/ })).toBeVisible();
  expect(await violations()).toEqual([]);
});

test("search: a click goes to the result; a change opens its panel", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  await box(page).fill("home");
  await opts(page).filter({ hasText: "Host home.example" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("Changes to apply");
  await expect(dialog).toContainText("Host home.example");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();

  await box(page).fill("home");
  await opts(page).filter({ hasText: "home-extra" }).click();
  await expect(page).toHaveURL(/\/blocklists\.html#list=home-extra$/);
  expect(await violations()).toEqual([]);
});

test("search: an honest empty state, and an error said", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page, q => (q === "broken" ? [500, { error: "the zones could not be read" }]
    : { query: q, results: [], matched: {}, more: false, partial: q === "slow", searched: [], problems: q === "slow" ? ["blocking/lists.json: unreadable"] : [] }));
  await page.goto("./");
  await box(page).fill("zzz-nothing");
  await expect(res(page)).toContainText('Nothing matches "zzz-nothing"');
  await expect(opts(page)).toHaveCount(0);
  await page.keyboard.press("Enter"); // nothing to go to: stays
  await expect(page).toHaveURL(/\/$/);
  await box(page).fill("slow");
  await expect(res(page)).toContainText("The search ran out of time");
  await expect(res(page)).toContainText("Couldn't search blocking/lists.json: unreadable");
  await box(page).fill("broken");
  await expect(res(page).locator(".search-note.bad")).toHaveText("the zones could not be read");
  expect(await violations()).toEqual([]);
});

test("search: Esc closes the list, then empties and leaves the box", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  await box(page).fill("home");
  await expect(res(page)).toBeVisible();
  await box(page).press("Escape");
  await expect(res(page)).toBeHidden();
  await expect(box(page)).toHaveValue("home");
  await expect(box(page)).toHaveAttribute("aria-expanded", "false");
  await box(page).press("ArrowDown"); // opens it again
  await expect(res(page)).toBeVisible();
  await box(page).press("Escape");
  await box(page).press("Escape");
  await expect(box(page)).toHaveValue("");
  await expect(box(page)).not.toBeFocused();
  // "/" typed into a field is that field's.
  await page.getByRole("button", { name: "+ Add node" }).click();
  await expect(page.locator("#nq")).toBeFocused();
  await page.keyboard.type("a/b");
  await expect(page.locator("#nq")).toHaveValue("a/b");
  await expect(box(page)).not.toBeFocused();
  expect(await violations()).toEqual([]);
});

test("search: at 375 px it is an icon that opens the box", async ({ page }) => {
  const violations = await watch(page);
  await page.setViewportSize({ width: 375, height: 812 });
  await login(page);
  await fake(page);
  await page.goto("./");
  const icon = page.getByRole("button", { name: "Search", exact: true });
  await expect(icon).toBeVisible();
  await expect(box(page)).toBeHidden();
  await icon.click();
  await expect(box(page)).toBeVisible();
  await expect(box(page)).toBeFocused();
  await expect(icon).toBeHidden();
  await page.keyboard.type("home");
  await expect(opts(page)).toHaveCount(RESULTS.length);
  const r = await res(page).boundingBox();
  expect(r.x).toBeGreaterThanOrEqual(0);
  expect(r.x + r.width).toBeLessThanOrEqual(375);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await expect(box(page)).toBeHidden();
  await expect(icon).toBeVisible();
  await page.keyboard.press("/");
  await expect(box(page)).toBeFocused();
  expect(await violations()).toEqual([]);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`search screenshots: ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 900 });
      await login(page);
      await fake(page, q => (q === "home" ? ANSWER : { query: q, results: [], matched: {}, more: false, partial: false, searched: [], problems: [] }));
      await page.goto("./");
      const dir = path.join(shots, "search");
      await page.screenshot({ path: path.join(dir, `bar-${width}-${scheme}.png`) });
      await page.keyboard.press("/");
      await page.keyboard.type("home");
      await expect(opts(page)).toHaveCount(RESULTS.length);
      await page.keyboard.press("ArrowDown");
      await page.keyboard.press("ArrowDown");
      await page.screenshot({ path: path.join(dir, `results-${width}-${scheme}.png`) });
      await box(page).fill("zzz-nothing");
      await expect(res(page)).toContainText("Nothing matches");
      await page.screenshot({ path: path.join(dir, `empty-${width}-${scheme}.png`) });
    });
  }
}
