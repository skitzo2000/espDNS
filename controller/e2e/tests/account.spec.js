// The account panel (account.js, opened from the top bar's account chip): the password, a
// backup with the password asked again, how to restore, the recent changes
// and logging out. serve.sh's controller answers the login, the password grant and the
// backup itself (its own throwaway data directory); the action log, the release key and
// the password change are answered here (page.route), so the login the other tests use
// stays as it is. E2E_SHOTS=<dir> also takes the panel at 375 and 1366 px, light and dark,
// into <dir>/account/.
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

// The keys as GET /api/key gives them (opts.key), and the release key's public half, a 65-byte
// point (P-256's generator: a public test value).
const RELFP = "a1b2c3d4e5f60718", RECFP = "0f1e2d3c4b5a6978";
const RELPUB = Buffer.from("046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5", "hex");

const ago = (days, h = 0) => new Date(Date.now() - days * 86400000 - h * 3600000).toISOString();

// The action log, newest first, as GET /api/actions answers it.
function actions(user) {
  return [
    { time: ago(0, 0.1), event: "end", id: "j3", source: "controller", who: user, action: "job apply", result: "ok" },
    { time: ago(0, 0.2), event: "start", id: "j3", source: "controller", who: user, action: "job apply" },
    { time: ago(0, 0.3), event: "save", id: "change-1", source: "controller", who: user, action: "change add", args: ["c1", "lists", "x", "Always allow a site"], result: "ok" },
    { time: ago(1), event: "end", id: "j2", source: "controller", who: user, action: "job check", result: "ok" },
    { time: ago(1, 1), event: "save", id: "settings-1", source: "controller", who: user, action: "settings", args: ["upstream DNS"], result: "ok" },
    { time: ago(2), event: "end", id: "cli-1", source: "cli", who: "ops@host", action: "rollout", result: "failed", error: "dns3 didn't answer" },
    { time: ago(9), event: "save", id: "zone-1", source: "controller", who: user, action: "zone save", args: ["home.arpa", "12 records"], result: "ok" },
  ];
}

async function fake(page, opts = {}) {
  const state = { password: [] };
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  await page.route(/\/api\/actions(\?.*)?$/, r => json(r, opts.actions || actions(process.env.E2E_USER)));
  if (opts.key) {
    await page.route(/\/api\/key$/, r => json(r, { present: true, source: "file", fingerprint: RELFP, recovery: { present: true, fingerprint: RECFP }, nodes_listed: true }));
    await page.route(/\/api\/keys\/release\.pub$/, r => r.fulfill({ status: 200, contentType: "application/octet-stream", body: RELPUB }));
  }
  await page.route(/\/api\/password$/, r => {
    state.password.push({ body: r.request().postDataJSON(), reauth: r.request().headers()["x-reauth"] || "" });
    if (opts.passwordError) return json(r, { error: opts.passwordError, field: "password" }, 400);
    return json(r, { logged_in: false });
  });
  if (opts.backupError) {
    await page.route(/\/api\/backup$/, r => r.request().method() === "POST"
      ? json(r, { error: opts.backupError }, 409) : r.continue());
  }
  return state;
}

async function openAccount(page) {
  await page.getByRole("button", { name: `Account: ${process.env.E2E_USER}` }).click();
  const d = page.getByRole("dialog", { name: process.env.E2E_USER });
  await expect(d).toBeVisible();
  return d;
}

// row is a row's summary in the panel, by its title.
const row = (d, title) => d.locator("details.ex > summary", { has: d.page().locator("b", { hasText: new RegExp(`^${title}$`) }) });

// The password asked again (common.js askPassword), answered with the real one.
async function giveRealPassword(page) {
  const ask = page.locator("dialog.reauth");
  await expect(ask).toBeVisible();
  await expect(ask.getByRole("heading")).toHaveText("Your password again");
  await ask.locator('input[type="password"]').fill(process.env.E2E_PASSWORD);
  await ask.getByRole("button", { name: "Continue" }).click();
}

test("Account: the panel's rows, the recent changes in words, each opening its place", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page);
  await page.goto("./");
  const d = await openAccount(page);
  const rows = d.locator("details.ex > summary > b");
  await expect(rows).toHaveText(["Password", "Keep the controller safe", "Rotate keys", "Firmware", "Recent changes"]);
  await expect(d.locator("details.ex").first()).not.toHaveAttribute("open", "");

  // serve.sh's data directory has no release key and no backup yet.
  await expect(d.locator("#safe-val")).toHaveText("No release key · No recovery key · Last backup: never");
  await expect(d.locator("#acct-release-fp")).toHaveText("none yet");
  await expect(d.locator("#acct-release-pub")).toBeDisabled();

  // Recent changes: the apply, the settings saved, the CLI's failed rollout, the zone; not
  // the job's start, the check or the change only queued.
  await expect(d.locator("#changes-val")).toHaveText("3 this week");
  await row(d, "Recent changes").click();
  const ch = d.locator("#changes-list .change");
  await expect(ch).toHaveCount(4);
  await expect(ch.nth(0)).toContainText("Changes applied");
  await expect(ch.nth(0)).toContainText(/Today \d\d:\d\d · You/);
  await expect(ch.nth(1)).toContainText("Settings changed: upstream DNS");
  await expect(ch.nth(1)).toContainText(/Yesterday/);
  await expect(ch.nth(2).locator(".bad")).toHaveText("Sending to the nodes failed");
  await expect(ch.nth(2)).toContainText("ops@host (command line)");
  await expect(ch.nth(3)).toContainText("Zone home.arpa saved");
  // A change about a zone opens it; four entries: no Show all.
  await expect(ch.nth(3).getByRole("link", { name: "Zone home.arpa saved" })).toHaveAttribute("href", "zones.html#zone=home.arpa");
  await expect(d.locator("#changes-all")).toBeHidden();
  // No older tools, no page that isn't there.
  await expect(d.locator("a[href$='.html']:not([href^='zones.html']):not([href^='node.html'])")).toHaveCount(0);

  // Restore: the CLI's, named with where its steps are, no make.
  await row(d, "Keep the controller safe").click();
  await expect(d.locator(".restore")).toContainText("espdns restore: docs/operations.md, Restore");
  await expect(d).not.toContainText("make ");

  // Closed by Esc, then by its x.
  await page.keyboard.press("Escape");
  await expect(d).toBeHidden();
  const d2 = await openAccount(page);
  await d2.getByRole("button", { name: "Close" }).click();
  await expect(d2).toBeHidden();
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Account: the scheduled blocklist refresh, sent and refused, by the controller", async ({ page }) => {
  await login(page);
  await fake(page, { actions: [
    { time: ago(0, 0.1), event: "end", id: "j5", source: "controller", who: "", action: "job refresh", args: ['{"lists":["main"]}'], result: "failed",
      error: "refused by the size-change check: main (blocked 2587385 → 12040 (-99.5%)); nothing was sent, the nodes keep the lists they have" },
    { time: ago(1), event: "end", id: "j4", source: "controller", who: "", action: "job refresh", args: ['{"lists":["main"]}'], result: "ok" },
  ] });
  await page.goto("./");
  const d = await openAccount(page);
  await row(d, "Recent changes").click();
  const ch = d.locator("#changes-list .change");
  await expect(ch).toHaveCount(2);
  await expect(ch.nth(0).locator(".bad")).toHaveText("Blocklist update refused: it changed more than its limit");
  await expect(ch.nth(0)).toContainText("The controller");
  await expect(ch.nth(1)).toContainText("Blocklists updated to their new versions");
  await expect(ch.nth(1).locator(".bad")).toHaveCount(0);
});

test("Account: no changes yet", async ({ page }) => {
  await login(page);
  await fake(page, { actions: [] });
  await page.goto("./");
  const d = await openAccount(page);
  await expect(d.locator("#changes-val")).toHaveText("None yet");
  await row(d, "Recent changes").click();
  await expect(d.locator("#changes-list")).toContainText("Nothing changed yet");
});

// The keys: both fingerprints, the release key's public half to download; nothing about a
// private key but keeping recovery.pem offline.
test("Account: the release and recovery fingerprints, release.pub to download", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page, { key: true });
  await page.goto("./");
  const d = await openAccount(page);
  await row(d, "Keep the controller safe").click();
  await expect(d.locator("#acct-release-fp")).toHaveText(RELFP);
  await expect(d.locator("#acct-recovery-fp")).toHaveText(RECFP);
  await expect(d.locator("#acct-keys")).toContainText("Keep recovery.pem offline");
  await expect(d.locator("#acct-keys")).not.toContainText(/private|release\.pem/i);
  const download = page.waitForEvent("download");
  await d.locator("#acct-release-pub").click();
  const dl = await download;
  expect(dl.suggestedFilename()).toBe("release.pub");
  expect(require("node:fs").readFileSync(await dl.path()).equals(RELPUB)).toBe(true);
  expect(await violations()).toEqual([]);
});

test("Account: a backup downloads after the password is given again", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page, { key: true });
  await page.goto("./");
  const d = await openAccount(page);
  await expect(d.locator("#safe-val")).toHaveText("Last backup: never");
  await row(d, "Keep the controller safe").click();

  // Checked before anything is asked.
  await d.locator("#bp").fill("short");
  await d.locator("#bp2").fill("short");
  await d.getByRole("button", { name: "Download backup" }).click();
  await expect(d.locator("#bp-msg")).toHaveText("The passphrase: at least 12 characters.");
  await d.locator("#bp").fill("a long backup passphrase");
  await d.locator("#bp2").fill("a different passphrase");
  await d.getByRole("button", { name: "Download backup" }).click();
  await expect(d.locator("#bp-msg")).toHaveText("The two passphrases differ.");

  // Esc in the password dialog cancels it and leaves the panel open.
  await d.locator("#bp2").fill("a long backup passphrase");
  await d.getByRole("button", { name: "Download backup" }).click();
  await expect(page.locator("dialog.reauth")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator("dialog.reauth")).toHaveCount(0);
  await expect(d.locator("#bp-msg")).toHaveText("Not downloaded.");
  await expect(d).toBeVisible();

  // Cancelled: nothing downloaded.
  await d.locator("#bp2").fill("a long backup passphrase");
  await d.getByRole("button", { name: "Download backup" }).click();
  await page.locator("dialog.reauth").getByRole("button", { name: "Cancel" }).click();
  await expect(d.locator("#bp-msg")).toHaveText("Not downloaded.");

  // Given: the controller writes it and the browser saves it.
  const dl = page.waitForEvent("download");
  await d.getByRole("button", { name: "Download backup" }).click();
  await giveRealPassword(page);
  const file = await dl;
  expect(file.suggestedFilename()).toMatch(/^espdns-backup-\d{8}-\d{6}\.age$/);
  await expect(d.locator("#bp-msg")).toContainText("Saved espdns-backup-");
  await expect(d.locator("#bp")).toHaveValue("");
  await expect(d.locator("#safe-val")).toHaveText(/^Last backup: today \d\d:\d\d$/);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Account: the password changed, then the login", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  const d = await openAccount(page);
  await row(d, "Password").click();
  await expect(d.getByText("Changing it signs everyone out.")).toBeVisible();
  await d.locator("#pw1").fill("too short");
  await d.locator("#pw2").fill("too short");
  await d.getByRole("button", { name: "Change password" }).click();
  await expect(d.locator("#pw-msg")).toHaveText("At least 12 characters.");
  await d.locator("#pw1").fill("a new password here");
  await d.locator("#pw2").fill("a new password there");
  await d.getByRole("button", { name: "Change password" }).click();
  await expect(d.locator("#pw-msg")).toHaveText("The two passwords differ.");
  expect(state.password).toEqual([]);

  await d.locator("#pw2").fill("a new password here");
  await d.getByRole("button", { name: "Change password" }).click();
  await giveRealPassword(page);
  await expect(page).toHaveURL(/login\.html/);
  expect(state.password).toHaveLength(1);
  expect(state.password[0].body).toEqual({ password: "a new password here" });
  expect(state.password[0].reauth).not.toBe("");
  expect(await page.evaluate(() => localStorage.getItem("espdns.session"))).toBeNull();
  expect(await violations()).toEqual([]);
});

test("Account: refusals said as the controller gives them; logged strings stay text", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const evil = '<img src=x id=pwned onerror="window.__pwned=1">';
  await fake(page, {
    passwordError: "password: too long",
    backupError: "the fleet lock is held by a rollout",
    actions: [{ time: ago(0, 0.1), event: "save", id: "zone-2", source: "controller", who: evil, action: "zone save", args: [evil, "1 record"], result: "ok" }],
  });
  await page.goto("./");
  const d = await openAccount(page);

  await row(d, "Recent changes").click();
  await expect(d.locator("#changes-list .change").first()).toContainText(`Zone ${evil} saved`);
  await expect(page.locator("#pwned")).toHaveCount(0);
  await row(d, "Recent changes").click();

  await row(d, "Password").click();
  await d.locator("#pw1").fill("a new password here");
  await d.locator("#pw2").fill("a new password here");
  await d.getByRole("button", { name: "Change password" }).click();
  await giveRealPassword(page);
  await expect(d.locator("#pw-msg")).toHaveText("password: too long");
  await expect(d.locator("#pw-msg")).toHaveClass(/bad/);
  await expect(page).not.toHaveURL(/login\.html/);
  await row(d, "Password").click();

  await row(d, "Keep the controller safe").click();
  await d.locator("#bp").fill("a long backup passphrase");
  await d.locator("#bp2").fill("a long backup passphrase");
  await d.getByRole("button", { name: "Download backup" }).click();
  await giveRealPassword(page);
  await expect(d.locator("#bp-msg")).toHaveText("the fleet lock is held by a rollout");
  await expect(d.locator("#bp-msg")).toHaveClass(/bad/);
  expect(await page.evaluate(() => window.__pwned)).toBeUndefined();
  expect(await violations()).toEqual([]);
});

test("Account: log out", async ({ page }) => {
  await login(page);
  await fake(page);
  await page.goto("./");
  const d = await openAccount(page);
  await d.getByRole("button", { name: "Log out" }).click();
  await expect(page).toHaveURL(/login\.html/);
  const s = await page.evaluate(async () => (await fetch("api/session")).json());
  expect(s.logged_in).toBe(false);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`Account screenshots: ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      const dir = path.join(shots, "account");
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: width < 600 ? 812 : 768 });
      await login(page);
      await fake(page, { key: true });
      await page.goto("./");
      const d = await openAccount(page);
      await expect(d.locator("#changes-val")).toHaveText("3 this week");
      await page.screenshot({ path: path.join(dir, `account-${width}-${scheme}.png`) });
      for (const [title, name] of [["Password", "password"], ["Keep the controller safe", "safe"], ["Recent changes", "changes"]]) {
        await row(d, title).click();
        await row(d, title).scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(dir, `account-${name}-${width}-${scheme}.png`) });
        await row(d, title).click();
      }
      await row(d, "Keep the controller safe").click();
      await d.locator("#bp").fill("a long backup passphrase");
      await d.locator("#bp2").fill("a long backup passphrase");
      await d.getByRole("button", { name: "Download backup" }).click();
      await expect(page.locator("dialog.reauth")).toBeVisible();
      await page.screenshot({ path: path.join(dir, `account-reauth-${width}-${scheme}.png`) });
    });
  }
}
