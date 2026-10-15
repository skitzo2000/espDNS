// Install over USB, after the install (usb.js afterInstall): the panel follows ESP Web Tools'
// dialog (its _installState; the dialog here is a stand-in with that property, no hardware),
// then waits for the node on the network: POST /api/nodes/lookup at its address, answered
// here. Nothing for a minute (the page's clock moved on): what to check. It answers: the
// panel says so, and Add it now closes it and opens + Add node with it chosen, its address
// and gateway as given at the install. An install that fails, or a
// dialog closed partway, says so and leaves the Install button there.
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login } = require("./session");

const RELFP = "a1b2c3d4e5f60718", RECFP = "0f1e2d3c4b5a6978";

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

// routes answers the keys, the boards (their chip images in), the build, the lookup (from
// state.lookup) and the adoption's sources.
async function routes(page, state) {
  await page.route(/\/api\/key$/, r => r.fulfill({ json: { present: true, fingerprint: RELFP, recovery: { present: true, fingerprint: RECFP } } }));
  await page.route(/\/api\/boards$/, async r => {
    const res = await r.fetch();
    await r.fulfill({ response: res, json: (await res.json()).map(e => ({ ...e, image_ready: true })) });
  });
  await page.route(/\/api\/build$/, async r => {
    const name = r.request().postDataJSON().name;
    const list = await (await page.request.get("api/boards", { headers: { "X-Session-Token": await page.evaluate(() => localStorage.getItem("espdns.session")) } })).json();
    const board = list.find(e => e.board.name === name).board;
    await r.fulfill({ json: { id: `e2e-${name}`, board, chip: { image: board.image, version: "0.0.5", built: "2026-10-07", elf_sha256: "ab".repeat(32) },
      keys: { release: RELFP, recovery: RECFP } } });
  });
  await page.route(/\/api\/nodes\/lookup$/, r => {
    state.lookups.push(r.request().postDataJSON().address);
    return r.fulfill({ json: state.lookup });
  });
  await page.route(/\/api\/adopt$/, r => r.fulfill({ json: { configs: [], key: true, no_dhcp: [] } }));
}

async function prepare(page, board) {
  await page.goto("./");
  await page.getByRole("button", { name: "Install over USB", exact: true }).click();
  const u = page.getByRole("dialog", { name: "Install a board over USB" });
  await u.locator("#usb-board").selectOption(board);
  await u.locator("#usb-addr").fill("192.0.2.52/24");
  await u.locator("#usb-gw").fill("192.0.2.1");
  await u.locator("#usb-prepare").click();
  await expect(u.locator("#usb-go")).toBeVisible();
  await expect(u.locator("#usb-after")).toHaveText("Once it's installed, this panel waits for it to show up on your network.");
  return u;
}

// The installer's dialog, as ESP Web Tools adds it: the element on the body, its state set.
const dialogIn = page => page.evaluate(() => { window.__ewt = document.createElement("ewt-install-dialog"); document.body.append(window.__ewt); });
const installState = (page, state, message = "") => page.evaluate(([state, message]) => { window.__ewt._installState = { state, message }; }, [state, message]);
const dialogOut = page => page.evaluate(() => window.__ewt.remove());

test("Install over USB: installed, it waits at its address, says what to check, then Add it now chooses it in + Add node", async ({ page }) => {
  const violations = await watch(page);
  const state = { lookup: { address: "192.0.2.52", found: false, answered: false }, lookups: [] };
  await page.clock.install();
  await routes(page, state);
  await login(page);
  const u = await prepare(page, "ws-s3-eth");
  expect(state.lookups).toEqual([]); // nothing looked up before the install

  await dialogIn(page);
  await installState(page, "writing", "Writing progress: 10%");
  await expect(u.locator("#usb-after")).toHaveText("Installing. Keep the board plugged in until the installer says it's done.");
  await installState(page, "finished", "All done!");
  await expect(u.locator("#usb-installed")).toHaveText("Installed. Plug it into your network with an Ethernet cable; USB can stay in for power.");
  await expect(u.locator("#usb-waiting")).toHaveText("Waiting for it at 192.0.2.52…");
  await expect(u.locator("#usb-go")).toHaveText("Install again");
  await dialogOut(page); // closed after it finished: still waiting
  await expect(u.locator("#usb-waiting")).toBeVisible();
  await expect.poll(() => state.lookups.length).toBeGreaterThan(0);
  expect(state.lookups[0]).toBe("192.0.2.52");
  await expect(u.locator("#usb-wait-hint")).toBeHidden();

  // A minute on, nothing: what to check, still waiting.
  await page.clock.fastForward("01:05");
  const hint = u.locator("#usb-wait-hint");
  await expect(hint).toBeVisible();
  await expect(hint).toContainText("Nothing yet. Check the cable is in, and that 192.0.2.52 is free on your network and outside your router's DHCP pool.");
  await expect(hint).toContainText("Its gateway is 192.0.2.1. That has to be your router's address on this network.");
  await expect(u.locator("#usb-waiting")).toBeVisible();

  // It answers: the panel says so, with Add it now.
  state.lookup = { address: "192.0.2.52", found: true, node: { host: "192.0.2.52", id: "aa:bb:cc:00:00:52", mac: "aa:bb:cc:00:00:52",
    board: "ws-s3-eth", hostname: "espdns-000052", version: "0.0.5", net: "ethernet", address: "192.0.2.52/24", gateway: "192.0.2.1", next: "adopt" } };
  await page.clock.fastForward("00:05");
  await expect(u.locator("#usb-arrived")).toHaveText("It's on your network at 192.0.2.52. Next, add it to your nodes.");
  await expect(u.locator("#usb-waiting")).toHaveCount(0);
  if (process.env.E2E_SHOTS) {
    await u.locator("#usb-add").scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(process.env.E2E_SHOTS, "install-arrived.png") });
  }
  // Add it now: the panel closes, + Add node has it chosen, the address given at the install in.
  await u.getByRole("button", { name: "Add it now" }).click();
  await expect(u).toBeHidden();
  const list = page.locator("#nqres");
  await expect(list).toContainText("Found at 192.0.2.52 over Wired, firmware 0.0.5.");
  await expect(list.locator("#nnhow-fixed")).toBeChecked();
  await expect(list.locator("#nnip")).toHaveValue("192.0.2.52/24");
  await expect(list.locator("#nngw")).toHaveValue("192.0.2.1");
  await expect(list.getByRole("button", { name: "Add node" })).toBeVisible();
  // No config yet: a new name, nothing to read.
  await expect(list.locator("#nnname")).toHaveValue("+new");
  await expect(list.locator("#nnfile")).toBeHidden();
  if (process.env.E2E_SHOTS) await page.screenshot({ path: path.join(process.env.E2E_SHOTS, "install-add-it-now.png") });
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Install over USB: a Wi-Fi board says it joins your Wi-Fi; something else at the address is said at once", async ({ page }) => {
  const violations = await watch(page);
  const state = { lookup: { address: "192.0.2.52", found: false, answered: true, error: "not a node" }, lookups: [] };
  await routes(page, state);
  await login(page);
  const u = await prepare(page, "xiao-s3-sense");
  await dialogIn(page);
  await installState(page, "finished");
  await expect(u.locator("#usb-installed")).toHaveText("Installed. It joins the Wi-Fi network you enter in the installer, or uses Ethernet if its add-on is fitted. USB can stay in for power.");
  await expect(u.locator("#usb-wait-hint")).toContainText("Something else answers at 192.0.2.52, so that address is taken.");
  await expect(u.locator("#usb-waiting")).toBeVisible();
  // Closing the panel stops the waiting.
  await page.getByRole("button", { name: "Close" }).last().click();
  await expect(u).toBeHidden();
  const n = state.lookups.length;
  await page.waitForTimeout(4000);
  expect(state.lookups.length).toBe(n);
  expect(await violations()).toEqual([]);
});

test("Install over USB: an install that fails, or a dialog closed partway, says so; Install stays", async ({ page }) => {
  const violations = await watch(page);
  const state = { lookup: { address: "192.0.2.52", found: false }, lookups: [] };
  await routes(page, state);
  await login(page);
  const u = await prepare(page, "ws-s3-eth");
  await dialogIn(page);
  await installState(page, "erasing");
  await installState(page, "error", "Failed to initialize. Try resetting your device or holding the BOOT button while clicking INSTALL.");
  await expect(u.locator("#usb-failed")).toHaveText("The install stopped: Failed to initialize. Try resetting your device or holding the BOOT button while clicking INSTALL.");
  await expect(u.locator("#usb-after")).toContainText("Unplug the board, plug it back in, and press Install on the board over USB to try again.");
  await dialogOut(page);
  await expect(u.locator("#usb-failed")).toBeVisible();
  await expect(u.locator("#usb-go")).toBeEnabled();

  await dialogIn(page);
  await installState(page, "writing");
  await expect(u.locator("#usb-after")).toContainText("Installing.");
  await dialogOut(page);
  await expect(u.locator("#usb-failed")).toHaveText("The installer closed before it finished, so the board isn't ready.");
  await expect(u.locator("#usb-after")).toContainText("Press Install on the board over USB to start again.");
  await expect(u.locator("#usb-go")).toBeEnabled();
  expect(state.lookups).toEqual([]);
  expect(await violations()).toEqual([]);
});

// The real dialog, on a stubbed port: the state usb.js reads is a property of it.
test("Install over USB: ESP Web Tools' dialog has the _installState usb.js follows", async ({ page }) => {
  await page.addInitScript(() => {
    const port = {
      readable: new ReadableStream({ pull() { return new Promise(() => {}); } }),
      writable: new WritableStream({ write() {} }),
      async open() {}, async close() {}, async setSignals() {}, async forget() {},
      getInfo() { return { usbVendorId: 0x303a, usbProductId: 0x1001 }; },
      addEventListener() {}, removeEventListener() {},
    };
    const serial = { async requestPort() { return port; }, async getPorts() { return [port]; }, addEventListener() {}, removeEventListener() {} };
    Object.defineProperty(Navigator.prototype, "serial", { configurable: true, get: () => serial });
  });
  const state = { lookup: { address: "192.0.2.52", found: false }, lookups: [] };
  await routes(page, state);
  await login(page);
  const u = await prepare(page, "ws-s3-eth");
  await u.locator("#usb-go").click();
  await page.waitForFunction(() => customElements.get("ewt-install-dialog") !== undefined && document.querySelector("ewt-install-dialog") !== null);
  expect(await page.evaluate(() => "_installState" in document.querySelector("ewt-install-dialog"))).toBe(true);
});
