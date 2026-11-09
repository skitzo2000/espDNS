// Every page of the controller under its Content-Security-Policy (internal/websec): it loads
// with no violation and its scripts run. The same CSP on every page: nothing hashed, nothing
// from another origin, no inline style.
const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");

const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
  "connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";
const web = path.join(__dirname, "..", "..", "web");
const pages = fs.readdirSync(web).filter(f => f.endsWith(".html")).sort();

// Records every CSP violation, as the page reports it (the event) and as Chromium logs it.
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

const { login, apiGet } = require("./session");

// serve.sh's controller lists no node and browses no mDNS (-browse-mdns=false): one found
// here means it went on the network, to the nodes on this LAN.
test("no node is known", async ({ page }) => {
  await login(page);
  await page.waitForTimeout(3000); // a browse answers within a second or two
  const r = await apiGet(page, "api/nodes");
  expect(r.status).toBe(200);
  expect(r.json).toEqual([]);
});

test("the pages are found", () => {
  expect(pages).toContain("index.html");
  // The four pages, the node page, the first run and the login: no other.
  expect(pages).toEqual(["blocklists.html", "index.html", "login.html", "node.html", "querylog.html", "setup.html", "zones.html"]);
});

for (const name of pages.filter(p => p !== "login.html")) {
  test(`${name}: no CSP violation, its scripts run`, async ({ page }) => {
    const violations = await watch(page);
    await login(page);
    const r = await page.goto(name);
    expect(r.status()).toBe(200);
    expect(r.headers()["content-security-policy"]).toBe(CSP);
    expect(r.headers()["x-frame-options"]).toBe("DENY");
    // shell.js draws the top bar: the page's module ran.
    await expect(page.locator('nav[aria-label="Main"]')).toBeVisible();
    await page.waitForLoadState("networkidle");
    expect(await violations()).toEqual([]);
  });
}

test("login.html logged out: no CSP violation, the form shown", async ({ page }) => {
  const violations = await watch(page);
  const r = await page.goto("login.html");
  expect(r.headers()["content-security-policy"]).toBe(CSP);
  await expect(page.locator("#form")).toBeVisible();
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// The icons and the mascot are the controller's own files, served logged out (the login
// page shows them); every page names the same icons.
test("login.html logged out: the icons and Artie load", async ({ page }) => {
  const violations = await watch(page);
  await page.goto("login.html");
  const icons = await page.evaluate(() => [...document.querySelectorAll('link[rel="icon"], link[rel="apple-touch-icon"]')].map(l => l.getAttribute("href")));
  expect(icons).toEqual(["img/favicon.ico", "img/icon-192.png", "img/apple-touch-icon.png"]);
  for (const href of icons) {
    const r = await page.request.get(href);
    expect(r.status(), href).toBe(200);
    expect(r.headers()["content-type"], href).toMatch(/^image\//);
  }
  const artie = page.locator("header.top img.top-artie");
  await expect(artie).toHaveAttribute("title", "Artie A. Record");
  await expect(artie).toHaveAttribute("alt", "");
  await expect.poll(() => artie.evaluate(i => i.complete && i.naturalWidth)).toBeGreaterThan(0);
  expect(await violations()).toEqual([]);
});

// An error said under a form is in the theme's warning colour with a ✕ before it, in light
// and dark: never grey like a hint.
test("login.html: an error stands out, light and dark", async ({ page }) => {
  const violations = await watch(page);
  // Refused here, not by the controller: a real failed login would count against the
  // other tests' logins from this machine.
  await page.route("**/api/login", r => r.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: "wrong user or password" }) }));
  for (const scheme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto("login.html");
    await page.locator("#user").fill("admin");
    await page.locator("#password").fill("not the password at all");
    await page.locator("#submit").click();
    const msg = page.locator("#msg");
    await expect(msg).toHaveClass(/\bbad\b/);
    const look = await msg.evaluate(m => {
      const hint = document.createElement("span");
      hint.className = "hint";
      document.body.append(hint);
      const grey = getComputedStyle(hint).color;
      hint.remove();
      return { color: getComputedStyle(m).color, grey, warn: getComputedStyle(document.documentElement).getPropertyValue("--warn").trim(),
        icon: getComputedStyle(m, "::before").content };
    });
    expect(look.color, scheme).not.toBe(look.grey);
    expect(look.icon, scheme).toContain("✕");
    const hex = look.warn.match(/^#(..)(..)(..)$/).slice(1).map(x => parseInt(x, 16));
    expect(look.color, scheme).toBe(`rgb(${hex.join(", ")})`);
  }
  expect(await violations()).toEqual([]);
});

test("a page logged out: redirected to the login, with the headers", async ({ page }) => {
  const violations = await watch(page);
  const seen = [];
  page.on("response", r => seen.push(r));
  await page.goto("zones.html");
  await expect(page).toHaveURL(/login\.html/);
  const redirect = seen.find(r => r.status() === 303);
  expect(redirect, "the 303").toBeTruthy();
  expect(redirect.headers()["content-security-policy"]).toBe(CSP);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// openUSB opens Install over USB from its button on Nodes, as a click does.
async function openUSB(page) {
  const r = await page.goto("./");
  expect(r.headers()["content-security-policy"]).toBe(CSP);
  await page.getByRole("button", { name: "Install over USB", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Install a board over USB" })).toBeVisible();
}

// Prepare image (its chip image given as imported, the build answered for it): a board that
// may run on Wi-Fi (the build says so) gets the line saying the panel asks for the Wi-Fi
// network over USB; a wired one doesn't. The address field stays as it is.
test("Install over USB: a Wi-Fi board's install says it asks for the Wi-Fi network; a wired one's doesn't", async ({ page }) => {
  const violations = await watch(page);
  await page.route(/\/api\/boards$/, async r => {
    const res = await r.fetch();
    const list = (await res.json()).map(e => ({ ...e, image_ready: true }));
    await r.fulfill({ response: res, json: list });
  });
  await page.route(/\/api\/build$/, async r => {
    const name = r.request().postDataJSON().name;
    const list = await (await page.request.get("api/boards", { headers: { "X-Session-Token": await page.evaluate(() => localStorage.getItem("espdns.session")) } })).json();
    const board = list.find(e => e.board.name === name).board;
    await r.fulfill({ json: { id: `e2e-${name}`, board, chip: { image: board.image, version: "0.0.5", built: "2026-10-07", elf_sha256: "ab".repeat(32) },
      keys: { release: "a1b2c3d4e5f60718", recovery: "0f1e2d3c4b5a6978" }, wifi: name === "xiao-s3-sense" } });
  });
  // The keys a board is installed with (serve.sh's controller has none: Prepare would wait).
  await page.route(/\/api\/key$/, r => r.fulfill({ json: { present: true, fingerprint: "a1b2c3d4e5f60718", recovery: { present: true, fingerprint: "0f1e2d3c4b5a6978" } } }));
  await login(page);
  await openUSB(page);
  const u = page.getByRole("dialog", { name: "Install a board over USB" });
  for (const [board, wifi] of [["xiao-s3-sense", true], ["ws-s3-eth", false]]) {
    await u.locator("#usb-board").selectOption(board);
    await u.locator("#usb-name").fill("dns3");
    await u.locator("#usb-addr").fill("192.0.2.52/24");
    await u.locator("#usb-gw").fill("192.0.2.1");
    await u.locator("#usb-prepare").click();
    await expect(u.locator("#usb-go")).toBeVisible();
    await expect(u.locator("#usb-install")).toContainText("with your keys (release a1b2c3d4e5f60718, recovery 0f1e2d3c4b5a6978)");
    await expect(u.locator("#usb-addr")).toHaveValue("192.0.2.52/24");
    if (wifi) {
      await expect(u.locator("#usb-wifi")).toHaveText("After it installs, this panel asks for your Wi-Fi network and password; they go to the board over USB, not through the controller.");
    } else {
      await expect(u.locator("#usb-wifi")).toHaveCount(0);
    }
  }
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("a page can't be framed", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  // A frame of the dashboard in a page of its own origin: frame-ancestors 'none' refuses even that.
  await page.goto("zones.html");
  const loaded = await page.evaluate(() => new Promise(resolve => {
    const f = document.createElement("iframe");
    f.src = "index.html";
    f.addEventListener("load", () => {
      try { resolve(f.contentDocument !== null && f.contentDocument.querySelector("main") !== null); }
      catch { resolve(false); }
    });
    document.body.append(f);
  }));
  expect(loaded).toBe(false);
  expect((await violations()).some(v => /frame-ancestors/.test(v))).toBeTruthy();
});
