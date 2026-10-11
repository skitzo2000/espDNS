// The first run (setup.html, setup.js, setupcheck.js) and the login page in the shell's look,
// against a fake controller: serve.sh's controller serves the pages (a login first, as it has
// a password), and every API the pages read is answered here (page.route), the session
// included, so the page sees a fresh install; documentation addresses (RFC 5737) only, and
// nothing goes on the network. E2E_SHOTS=<dir> also takes each step at 375 and 1366 px,
// light and dark, into <dir>/firstrun/.
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", FOUND = "198.51.100.37", PRIMARY = "192.0.2.254";
const ORDER = ["password", "keys", "primary", "first_node", "second_node", "router", "blocking"];
const RELFP = "a1b2c3d4e5f60718", RECFP = "0f1e2d3c4b5a6978";

function node(addr, id, name) {
  return {
    id, addr, hostname: `${name}.local`, source: "settings", online: true, last_seen: new Date().toISOString(), qps: 0.2,
    status: { board: "p4-ip101", version: "0.0.4", uptime_s: 600, net: { kind: "ethernet" }, health: { state: "healthy", answering: true, reasons: [] }, config: { name } },
  };
}

// fake answers the APIs from state, which a test changes as the controller would; posts is
// what was sent. done: the steps done at the start.
async function fake(page, done = []) {
  // Off the page first: one still loading (the Nodes page after the login) would read these
  // answers and go to the setup itself, racing the test's own navigation.
  await page.goto("about:blank");
  await page.unrouteAll({ behavior: "ignoreErrors" });
  const state = {
    loggedIn: done.includes("password"), done: new Set(done), key: done.includes("keys"), recFP: done.includes("keys") ? RECFP : "",
    nodes: [], found: [{ host: FOUND, id: "aa:bb:cc:00:00:37", hostname: "espdns-000037.local", source: "mdns", online: true, read: true,
      board: "p4-ip101", version: "0.0.4", net: "ethernet", mac: "aa:bb:cc:00:00:3a", adopted: false, listed: false, configs: true, config: "dns2.json", next: "adopt" }],
    primary: null, blocklists: 0, posts: [], sent: [],
  };
  if (done.includes("first_node")) state.nodes.push(node(`${DNS2}:80`, "aa:bb:cc:00:00:02", "dns2"));
  if (done.includes("second_node")) state.nodes.push(node(`${DNS3}:80`, "aa:bb:cc:00:00:03", "dns3"));
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  const setup = () => {
    const steps = ORDER.map(s => ({ step: s, done: s === "password" ? state.loggedIn : s === "keys" ? state.key && !!state.recFP && state.done.has("keys")
      : s === "primary" ? !!state.primary || state.done.has("primary") : s === "first_node" ? state.nodes.length >= 1
        : s === "second_node" ? state.nodes.length >= 2 : state.done.has(s), optional: s === "primary" || s === "blocking" || undefined }));
    return { first_run: !state.loggedIn && !state.primary, password_set: state.loggedIn, settings_saved: !!state.primary, release_key: state.key,
      recovery_key: !!state.recFP, keys_saved: state.done.has("keys"), primary: state.primary ? state.primary.address : "", nodes: state.nodes.length, steps,
      done: steps.every(s => s.done), blocklists: state.blocklists };
  };
  // GET /api/key as keysapi.go answers it.
  const key = () => ({ source: "file", ...(state.key ? { present: true, fingerprint: RELFP } : { present: false, missing: true, error: "no release key" }),
    recovery: state.recFP ? { present: true, fingerprint: state.recFP } : { present: false, missing: true, error: "no recovery public key" },
    nodes_listed: state.nodes.length > 0 });
  await page.route(/\/api\/session$/, r => json(r, state.loggedIn ? { password_set: true, logged_in: true, user: "admin" } : { password_set: false, logged_in: false }));
  await page.route(/\/api\/setup$/, r => json(r, setup()));
  await page.route(/\/api\/setup\/password$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["password", b]);
    if (state.refuse) return json(r, { error: "the first password is set from the controller's own machine only" }, 403);
    state.loggedIn = true;
    return json(r, { user: b.user, token: "e2e-first-run-token", device: "d", expires: new Date(Date.now() + 3600e3).toISOString() });
  });
  await page.route(/\/api\/setup\/step$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["step", b]);
    state.done.add(b.step);
    return json(r, setup());
  });
  await page.route(/\/api\/key$/, r => json(r, key()));
  await page.route(/\/api\/keys\/release$/, r => {
    state.posts.push(["release", r.request().postDataJSON()]);
    state.key = true;
    return json(r, key());
  });
  // The recovery key: what was sent kept whole (raw), to check nothing but the public key went.
  await page.route(/\/api\/keys\/recovery$/, r => {
    const raw = r.request().postData();
    state.posts.push(["recovery", JSON.parse(raw)]);
    state.sent.push(raw);
    const pub = Buffer.from(JSON.parse(raw).public_key, "base64");
    if (pub.length !== 65 || pub[0] !== 4) return json(r, { error: "not a P-256 public key", field: "public_key" }, 400);
    state.recFP = crypto.createHash("sha256").update(pub).digest("hex").slice(0, 16);
    return json(r, key());
  });
  const settings = () => ({ settings: { nodes: state.nodes.map(n => n.addr), dns_peers: [], dns_peer_zones: [], canary: "", no_dhcp: [], primary: state.primary, internal_sources: [] },
    version: state.primary ? "v1" : "", exists: !!state.primary,
    primary: { set: !!state.primary, address: state.primary ? state.primary.address : "", allows: [], zones: [], uncovered: [] } });
  await page.route(/\/api\/settings$/, r => {
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON();
      state.posts.push(["settings", b]);
      state.primary = b.settings.primary;
    }
    return json(r, settings());
  });
  await page.route(/\/api\/nodes$/, r => json(r, state.nodes));
  await page.route(/\/api\/nodes\/found$/, r => json(r, { nodes: state.found }));
  await page.route(/\/api\/boards$/, r => json(r, [{ board: { name: "p4-ip101", title: "ESP32-P4 with Ethernet" } }]));
  await page.route(/\/api\/changes$/, r => json(r, { changes: [], restarts: [], apply: null }));
  await page.route(/\/api\/updates$/, r => json(r, { nodes: [] }));
  await page.route(/\/api\/dashboard$/, r => json(r, { fleet: {} }));
  return state;
}

const row = (page, step) => page.locator(`#step-${step}`);
// open loads url afresh (going to setup.html#next from setup.html would only change the hash).
async function open(page, url) {
  await page.goto("about:blank");
  await page.goto(url);
}

test("first run: the steps, then the password set from the browser", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("setup.html");
  await expect(page.locator("h1")).toHaveText("Set up espDNS");
  await expect(page.locator("#steps .s")).toHaveCount(7);
  await expect(page.locator("#steps .s b")).toHaveText(["Set a password", "Make your keys", "Set up your zone primary",
    "Add your first node", "Add a second node", "Tell your router", "Turn on blocking"]);
  await expect(page.locator("#facts")).toHaveText("0 of 7 steps done");
  // The read-only band isn't shown here: this page sets the password.
  await expect(page.locator(".band")).toHaveCount(0);
  await expect(page.locator(".s .btn.primary")).toHaveCount(1);
  await row(page, "password").getByRole("button", { name: "Start" }).click();
  await expect(page.locator("#su-user")).toHaveValue("admin");

  await page.locator("#su-pw").fill("short");
  await page.locator("#su-pw2").fill("short");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(row(page, "password").locator("[role=status]")).toHaveText("The password needs at least 12 characters.");
  await page.locator("#su-pw").fill("a long e2e password");
  await page.locator("#su-pw2").fill("a long e2e passwore");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(row(page, "password").locator("[role=status]")).toContainText("differ");
  expect(state.posts).toEqual([]);

  await page.locator("#su-pw2").fill("a long e2e password");
  await page.getByRole("button", { name: "Set password" }).click();
  // Logged in (the token kept), the page again with Make your keys open.
  await expect(row(page, "password")).toHaveClass(/\bdone\b/);
  await expect(row(page, "password")).toContainText("you're logged in as admin");
  expect(state.posts).toEqual([["password", { user: "admin", password: "a long e2e password" }]]);
  expect(await page.evaluate(() => localStorage.getItem("espdns.session"))).toBe("e2e-first-run-token");
  await expect(row(page, "keys").locator("#keys-release-make")).toBeVisible();
  await expect(page.locator("#facts")).toHaveText("1 of 7 steps done");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("first run: the password refused from another machine says where to set it", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  state.refuse = true;
  await page.goto("setup.html");
  await row(page, "password").getByRole("button", { name: "Start" }).click();
  await page.locator("#su-pw").fill("a long e2e password");
  await page.locator("#su-pw2").fill("a long e2e password");
  await page.getByRole("button", { name: "Set password" }).click();
  const msg = row(page, "password").locator("[role=status]");
  await expect(msg).toContainText("own machine only");
  await expect(msg).toContainText("espdns passwd");
  await expect(msg).not.toContainText("make ");
  expect(await violations()).toEqual([]);
});

// Make your keys: the release key made in the controller; the recovery key made in this
// browser (WebCrypto, headless Chromium), recovery.pem downloaded, and ONLY its public half
// posted: the request carries the 65-byte point and nothing of the private key; then
// "I saved recovery.pem" ticked, and the step done.
test("Make your keys: the release key in the controller, the recovery key in the browser, only its public half sent", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password"]);
  // Every request the page makes from here, to check none carries the private key.
  const bodies = [];
  page.on("request", r => bodies.push(`${r.method()} ${r.url()} ${r.postData() || ""}`));
  await page.goto("setup.html");
  await row(page, "keys").getByRole("button", { name: "Start" }).click();
  const body = row(page, "keys").locator(".sb");
  await expect(body).not.toContainText(/\bnull\b|undefined/);
  // Nothing made yet: the recovery key waits for the release key; Done waits for both.
  await expect(body.locator("#keys-recovery-make")).toBeDisabled();
  await expect(body.locator("#keys-saved")).toBeDisabled();
  await expect(body.locator("#keys-done")).toBeDisabled();
  await expect(body.locator("#keys-import pre.cmd")).toContainText("key import -data /data");

  await body.locator("#keys-release-make").click();
  await expect(body.locator("#keys-release-fp")).toHaveText(RELFP);
  expect(state.posts).toEqual([["release", {}]]);

  const download = page.waitForEvent("download");
  await body.locator("#keys-recovery-make").click();
  const dl = await download;
  expect(dl.suggestedFilename()).toBe("recovery.pem");
  const pem = fs.readFileSync(await dl.path(), "utf8");
  expect(pem).toMatch(/^-----BEGIN PRIVATE KEY-----\n[A-Za-z0-9+/=\n]+-----END PRIVATE KEY-----\n$/);
  // A P-256 PKCS#8 key that Node reads, whose public half is the one posted.
  const priv = crypto.createPrivateKey(pem);
  expect(priv.asymmetricKeyDetails.namedCurve).toBe("prime256v1");
  const pubRaw = crypto.createPublicKey(priv).export({ format: "der", type: "spki" }).subarray(-65);
  await expect(body.locator("#keys-recovery-fp")).toHaveText(state.recFP);
  expect(state.posts[1][0]).toBe("recovery");
  expect(Object.keys(state.posts[1][1]).sort()).toEqual(["public_key", "replace"]);
  expect(Buffer.from(state.posts[1][1].public_key, "base64").equals(pubRaw)).toBe(true);
  expect(state.recFP).toBe(crypto.createHash("sha256").update(pubRaw).digest("hex").slice(0, 16));
  // Nothing sent anywhere holds the private key: not the PEM, its base64 body, nor the scalar.
  const pemBody = pem.split("\n").filter(l => l && !l.startsWith("-----")).join("");
  const d = priv.export({ format: "jwk" }).d;
  for (const sent of [...state.sent, ...bodies]) {
    expect(sent).not.toContain("PRIVATE");
    expect(sent).not.toContain(pemBody.slice(0, 40));
    expect(sent).not.toContain(d);
    expect(sent).not.toContain(Buffer.from(d, "base64url").toString("base64"));
    expect(sent).not.toContain(Buffer.from(d, "base64url").toString("hex"));
  }
  // The file again, while the page is open.
  await expect(body.locator("#keys-recovery-again")).toBeVisible();

  // Done once recovery.pem is said to be saved.
  await expect(body.locator("#keys-done")).toBeDisabled();
  await body.locator("#keys-saved").check();
  await body.locator("#keys-done").click();
  await expect(row(page, "keys")).toHaveClass(/\bdone\b/);
  await expect(row(page, "keys")).toContainText(`Release ${RELFP} · recovery ${state.recFP} · recovery.pem kept offline`);
  expect(state.posts.at(-1)).toEqual(["step", { step: "keys" }]);
  // The next step opens; Back it up opens the account panel at the backup.
  await expect(row(page, "primary").locator("#pa")).toBeVisible();
  await row(page, "keys").getByRole("button", { name: "Back it up" }).click();
  await expect(page.locator("aside.drawer #acct-safe")).toHaveAttribute("open", "");
  expect(await violations()).toEqual([]);
});

// A recovery key you have: its public key file (raw 65 bytes, or a PEM public key) read in the
// browser and posted; a private key file refused there and never sent.
test("Make your keys: a recovery public key file, a private key file refused", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password"]);
  state.key = true;
  await page.goto("setup.html");
  await row(page, "keys").getByRole("button", { name: "Start" }).click();
  const body = row(page, "keys").locator(".sb");
  await expect(body.locator("#keys-release-fp")).toHaveText(RELFP);
  await body.locator("#keys-recovery-import summary").click();
  const { privateKey, publicKey } = crypto.generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const privPem = privateKey.export({ format: "pem", type: "pkcs8" });
  await body.locator("#keys-recovery-file").setInputFiles({ name: "recovery.pem", mimeType: "application/x-pem-file", buffer: Buffer.from(privPem) });
  await expect(body.locator("[role=status]")).toContainText("That's a private key");
  expect(state.posts).toEqual([]);
  // The PEM public key: the same point as the raw file.
  const raw = publicKey.export({ format: "der", type: "spki" }).subarray(-65);
  await body.locator("#keys-recovery-file").setInputFiles({ name: "recovery.pub", mimeType: "application/octet-stream",
    buffer: Buffer.from(publicKey.export({ format: "pem", type: "spki" })) });
  await expect(body.locator("#keys-recovery-fp")).toHaveText(crypto.createHash("sha256").update(raw).digest("hex").slice(0, 16));
  expect(state.posts).toEqual([["recovery", { public_key: raw.toString("base64") }]]);
  expect(await violations()).toEqual([]);
});

// With nodes listed already (an existing fleet): the release key isn't made here (they trust
// one: import it), and the recovery key is the one they trust, as a file.
test("Make your keys: an existing fleet imports its keys", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page, ["password", "first_node"]);
  await page.goto("setup.html");
  await row(page, "keys").getByRole("button", { name: "Start" }).click();
  const body = row(page, "keys").locator(".sb");
  await expect(body.locator("#keys-release-make")).toHaveCount(0);
  await expect(body).toContainText("Your nodes trust a release key already: import that one.");
  await expect(body.locator("#keys-import")).toHaveAttribute("open", "");
  await expect(body.locator("#keys-recovery-import")).toHaveAttribute("open", "");
  expect(await violations()).toEqual([]);
});

// The zone primary: its address, the networks it allows zone transfers from (the nodes in
// none of them said), the zones to copy; no API, token or certificate. Refused in place
// before anything is sent when the address isn't one.
test("the zone primary: its address, the networks it allows, the zones to copy", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password", "keys", "first_node"]);
  await page.goto("setup.html");
  await row(page, "primary").getByRole("button", { name: "Start" }).click();
  const r = row(page, "primary");
  await expect(r).not.toContainText(/token|certificate|https/i);
  await page.locator("#pa").fill("primary.example");
  await r.getByRole("button", { name: "Save" }).click();
  await expect(r.locator(".bad").filter({ hasText: "isn't an IPv4 address" })).toBeVisible();
  expect(state.posts).toEqual([]);

  await page.locator("#pa").fill(PRIMARY);
  await expect(page.locator("#pn-uncovered")).toContainText(`Not in these networks: ${DNS2}. Allow its network on your primary (e.g. 192.0.2.0/24)`);
  await page.locator("#pn").fill("192.0.2.0/24");
  await page.locator("#pn-add").click();
  await expect(page.locator("#pn-uncovered")).toHaveText("Every node's address is in these networks.");
  await page.locator("#pz").fill("home.example");
  await page.locator("#pz").press("Enter");
  await expect(page.locator("#pz-list li .mono")).toHaveText(["home.example"]);
  await r.getByRole("button", { name: "Save" }).click();
  await expect(r).toHaveClass(/\bdone\b/);
  expect(state.posts).toEqual([["settings", { version: "", settings: expect.objectContaining({
    primary: { address: PRIMARY, allows: ["192.0.2.0/24"], zones: ["home.example"] } }) }]]);
  await expect(r).toContainText(`${PRIMARY} · allows 192.0.2.0/24 · 1 zone to copy`);
  expect(await violations()).toEqual([]);
});

test("the zone primary skipped, then set up after all", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password", "keys"]);
  await page.goto("setup.html");
  await row(page, "primary").getByRole("button", { name: "Start" }).click();
  await page.getByRole("button", { name: "Skip: no zone primary" }).click();
  await expect(row(page, "primary")).toHaveClass(/\bdone\b/);
  await expect(row(page, "primary")).toContainText("Skipped");
  expect(state.posts).toEqual([["step", { step: "primary" }]]);

  await row(page, "primary").getByRole("button", { name: "Set up" }).click();
  await page.locator("#pa").fill(PRIMARY);
  await row(page, "primary").getByRole("button", { name: "Save" }).click();
  await expect(row(page, "primary")).toContainText(PRIMARY);
  expect(state.posts[1][1].settings.primary).toEqual({ address: PRIMARY, allows: [], zones: [] });
  expect(await violations()).toEqual([]);
});

test("the nodes: + Add node lists the one found; the second step names the first node", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page, ["password", "keys", "primary"]);
  await page.goto("setup.html");
  await row(page, "first_node").getByRole("button", { name: "Start" }).click();
  await expect(row(page, "first_node")).toContainText("One node is on your network and not added yet.");
  await row(page, "first_node").getByRole("button", { name: "+ Add node" }).click();
  await expect(page.locator("#nqres")).toContainText(FOUND);
  await expect(page.locator("#nqres")).toContainText("A new board over USB");
  await page.locator("#nq").press("Escape");
  expect(await violations()).toEqual([]);

  const state2 = await fake(page, ["password", "keys", "primary", "first_node"]);
  await open(page, "setup.html#next");
  await expect(row(page, "first_node")).toContainText(`dns2 at ${DNS2}`);
  await expect(row(page, "second_node").locator(".sb")).toContainText("dns2 is your first node");
  expect(state2.posts).toEqual([]);
});

test("the router and blocking, then set up", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password", "keys", "primary", "first_node", "second_node"]);
  await page.goto("setup.html");
  await row(page, "router").getByRole("button", { name: "Start" }).click();
  const body = row(page, "router").locator(".sb");
  await expect(body.locator(".ip")).toHaveText([DNS2, DNS3]);
  await body.getByRole("button", { name: "Done" }).click();
  await expect(row(page, "router")).toHaveClass(/\bdone\b/);
  const b = row(page, "blocking").locator(".sb");
  await expect(b.getByRole("link", { name: "Choose lists" })).toHaveAttribute("href", "blocklists.html");
  await b.getByRole("button", { name: "Skip: no blocking" }).click();
  await expect(page.locator("h1")).toHaveText("espDNS is set up");
  await expect(page.locator("#intro")).toContainText("dns2 and dns3");
  await expect(page.getByRole("link", { name: "Go to your nodes" })).toBeVisible();
  await expect(page.locator("#facts")).toHaveText("7 of 7 steps done");
  expect(state.posts).toEqual([["step", { step: "router" }], ["step", { step: "blocking" }]]);
  expect(await violations()).toEqual([]);
});

test("Nodes: a first run goes to the setup; after it, a checklist until done", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await expect(page).toHaveURL(/setup\.html$/);
  state.loggedIn = true;
  state.key = true;
  state.recFP = RECFP;
  state.done.add("keys");
  await page.goto("./");
  const left = page.locator("#setup-left");
  await expect(left).toContainText("2 of 7 steps done · next: set up your zone primary");
  await expect(left.getByRole("link", { name: "Continue" })).toHaveAttribute("href", "setup.html#next");
  await left.getByRole("link", { name: "Continue" }).click();
  await expect(row(page, "primary").locator("#pa")).toBeVisible();
  for (const s of ["primary", "router", "blocking"]) state.done.add(s);
  state.nodes = [node(`${DNS2}:80`, "aa:bb:cc:00:00:02", "dns2"), node(`${DNS3}:80`, "aa:bb:cc:00:00:03", "dns3")];
  await page.goto("./");
  await expect(page.locator("#sentence")).toHaveText("DNS is answering on both nodes");
  await expect(page.locator("#setup-left")).toHaveCount(0);
  expect(await violations()).toEqual([]);
});

test("login page: in the shell's look; a first run goes to the setup", async ({ page }) => {
  const violations = await watch(page);
  await page.goto("login.html");
  await expect(page.locator("header.top .brand")).toHaveText("espDNS");
  await expect(page.locator("#form")).toBeVisible();
  await expect(page.locator("#submit")).toHaveClass(/\bprimary\b/);
  expect(await page.evaluate(() => [...document.styleSheets].map(s => s.href.split("/").pop()))).toEqual(["shell.css", "firstrun.css"]);
  // serve.sh's controller has a password: logged in, it serves setup.html; the page sees a
  // fresh install.
  await login(page);
  await fake(page);
  await page.goto("login.html");
  await expect(page).toHaveURL(/setup\.html$/);
  await expect(page.locator("#steps .s")).toHaveCount(7);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: first run, ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      const dir = path.join(shots, "firstrun");
      fs.mkdirSync(dir, { recursive: true });
      // From the top: a page scrolled to the open step would draw the sticky bar mid-shot.
      const shot = async name => {
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: path.join(dir, `${name}-${width}-${scheme}.png`), fullPage: true });
      };
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: 768 });
      await page.goto("login.html");
      await expect(page.locator("#form")).toBeVisible();
      await shot("login");
      await login(page);
      let state = await fake(page);
      await page.goto("setup.html");
      await expect(page.locator("#steps .s")).toHaveCount(7);
      await shot("steps");
      await row(page, "password").getByRole("button", { name: "Start" }).click();
      await shot("password");
      state = await fake(page, ["password"]);
      await open(page, "setup.html#next");
      await expect(row(page, "keys").locator("#keys-release-make")).toBeVisible();
      await shot("keys");
      // Both made (the recovery key in the browser), before recovery.pem is said to be saved.
      await row(page, "keys").locator("#keys-release-make").click();
      await expect(row(page, "keys").locator("#keys-recovery-make")).toBeEnabled();
      await row(page, "keys").locator("#keys-recovery-make").click();
      await expect(row(page, "keys").locator("#keys-recovery-fp")).toHaveText(/^[0-9a-f]{16}$/);
      await row(page, "keys").locator("#keys-saved").check();
      await shot("keys-made");
      state = await fake(page, ["password", "keys"]);
      await open(page, "setup.html#next");
      await page.locator("#pa").fill(PRIMARY);
      await page.locator("#pn").fill("192.0.2.0/24");
      await page.locator("#pn-add").click();
      await shot("primary");
      state = await fake(page, ["password", "keys", "primary"]);
      await open(page, "setup.html#next");
      await row(page, "first_node").getByRole("button", { name: "+ Add node" }).click();
      await expect(page.locator("#nqres")).toContainText(FOUND);
      await shot("first-node");
      state = await fake(page, ["password", "keys", "primary", "first_node", "second_node"]);
      await open(page, "setup.html#next");
      await expect(row(page, "router").locator(".ip")).toHaveCount(2);
      await shot("router");
      state = await fake(page, ["password", "keys", "primary", "first_node", "second_node", "router"]);
      await open(page, "setup.html#next");
      await shot("blocking");
      state.done.add("blocking");
      await open(page, "setup.html");
      await expect(page.locator("h1")).toHaveText("espDNS is set up");
      await shot("done");
      state = await fake(page, ["password", "keys"]);
      await page.goto("./");
      await expect(page.locator("#setup-left")).toBeVisible();
      await shot("nodes-checklist");
    });
  }
}
