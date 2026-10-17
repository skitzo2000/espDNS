// Install over USB, the install itself (usb.js, boardlog.js, flash.js), in the panel, on a
// fake board behind a stubbed Web Serial port (fakeboard.js): the port asked for first, the
// chip checked against the image's, one write of the image at 0, the board restarted, its
// console in the Board log, the Wi-Fi network asked for over Improv on a Wi-Fi board, then
// OK, which hands it to the controller to add once it answers (a waiting row on Nodes). The
// keys, the boards (their chip images in), the build, its image, the lookup and the boards
// waiting are answered here (page.route).
const fs = require("node:fs");
const path = require("node:path");
const { test, expect } = require("@playwright/test");
const { login } = require("./session");
const { fakeEsp } = require("./fakeboard");

const RELFP = "a1b2c3d4e5f60718", RECFP = "0f1e2d3c4b5a6978";
// The image the fake controller hands out: 48 KB, every byte telling where it is.
const IMAGE = Buffer.from(Array.from({ length: 0xc000 }, (_, i) => (i * 13 + (i >> 9)) & 0xff));

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

async function routes(page, state) {
  await page.route(/\/api\/key$/, r => r.fulfill({ json: { present: true, fingerprint: RELFP, recovery: { present: true, fingerprint: RECFP } } }));
  await page.route(/\/api\/boards$/, async r => {
    const res = await r.fetch();
    await r.fulfill({ response: res, json: (await res.json()).map(e => ({ ...e, image_ready: true })) });
  });
  await page.route(/\/api\/build$/, async r => {
    const name = r.request().postDataJSON().name;
    const list = await (await page.request.get("api/boards", { headers: { "X-Session-Token": await page.evaluate(() => localStorage.getItem("espdns.session")) } })).json();
    const board = list.find(e => e.board.name === name).board, eth = board.ethernet || {};
    await r.fulfill({ json: { id: `e2e-${name}`, board, wifi: !eth.kind || eth.kind === "none" || !!eth.optional,
      chip: { image: board.image, chip_family: board.image.startsWith("esp32p4") ? "ESP32-P4" : "ESP32-S3", version: "0.0.5", built: "2026-10-07", elf_sha256: "ab".repeat(32) },
      keys: { release: RELFP, recovery: RECFP } } });
  });
  await page.route(/\/build\/e2e-[^/]+\/image\.bin$/, r => { state.images++; return r.fulfill({ status: 200, contentType: "application/octet-stream", body: IMAGE }); });
  await page.route(/\/api\/nodes\/lookup$/, r => {
    state.lookups.push(r.request().postDataJSON().address);
    return r.fulfill({ json: state.lookup });
  });
  await page.route(/\/api\/adopt$/, r => r.fulfill({ json: { configs: [], key: true, no_dhcp: [] } }));
  await page.route(/\/api\/nodes\/waiting$/, r => {
    if (r.request().method() === "GET") return r.fulfill({ json: { nodes: state.waiting } });
    if (state.refuse) return r.fulfill({ status: 409, json: { error: state.refuse } });
    state.waiting.push({ ...r.request().postDataJSON(), since: "2026-10-17T12:00:00Z", state: "waiting" });
    return r.fulfill({ json: state.waiting.at(-1) });
  });
}

async function start(page, { board = "ws-s3-eth", address = "192.0.2.52/24", fake = {}, lookup = { address: "192.0.2.52", found: false } } = {}) {
  const violations = await watch(page);
  await page.addInitScript(fakeEsp, fake);
  const state = { lookup, lookups: [], images: 0, waiting: [] };
  await routes(page, state);
  await login(page);
  await page.goto("./");
  await page.getByRole("button", { name: "Install over USB", exact: true }).click();
  const u = page.getByRole("dialog", { name: "Install a board over USB" });
  await u.locator("#usb-board").selectOption(board);
  // A new board gets a name first.
  await u.locator("#usb-addr").fill(address);
  await u.locator("#usb-gw").fill("192.0.2.1");
  await u.locator("#usb-prepare").click();
  await expect(u.locator("#usb-install")).toHaveText("Give the node a name: lowercase letters, digits and dashes, like dns3.");
  await u.locator("#usb-name").fill("dns3");
  await u.locator("#usb-gw").fill("192.0.2.1");
  await u.locator("#usb-prepare").click();
  await expect(u.locator("#usb-go")).toHaveText("Install on the board over USB");
  await expect(u.locator("#usb-erase")).not.toBeChecked();
  return { u, state, violations };
}

const esp = page => page.evaluate(() => ({ ops: window.__esp.ops, begins: window.__esp.begins, flash: window.__esp.flash, join: window.__esp.join }));
const step = (u, n) => u.locator(`#usb-step-${n}`);

test("Install over USB: a wired board written at 0, restarted, its log line by line, then OK adds it to Nodes", async ({ page }) => {
  const { u, state, violations } = await start(page);
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-installed")).toHaveText("Installed. Plug it into your network: it's added to your nodes as soon as it answers.", { timeout: 20000 });
  for (const n of ["connect", "chip", "write", "restart"]) await expect(step(u, n)).toHaveClass("done");
  await expect(step(u, "erase")).toHaveCount(0);
  await expect(step(u, "wifi")).toHaveCount(0);
  await expect(step(u, "connect")).toContainText("Over the board's own USB: ESP32-S3");
  await expect(step(u, "chip")).toContainText("ESP32-S3");
  await expect(step(u, "write")).toContainText("100%");
  await expect(step(u, "net")).toHaveClass("next");
  await expect(step(u, "net")).toContainText("Plug its network cable in.");
  await expect(u.locator("#usb-waiting")).toHaveCount(0);
  await expect(u.locator("#usb-reserve")).toHaveCount(0);
  await expect(u.locator("#usb-ok")).toHaveClass(/primary/);
  await expect(u.locator("#usb-go")).toHaveText("Install again");
  await expect(u.locator("#usb-go")).not.toHaveClass(/primary/);
  await expect(u.locator("#usb-hints")).toBeEmpty();

  // One compressed write of the image at 0, no erase, the bytes the controller made.
  const e = await esp(page);
  expect(e.ops).not.toContain(0xd0);
  expect(e.ops).not.toContain(0xd1);
  expect(e.begins).toEqual([{ offset: 0, size: IMAGE.length }]);
  expect(Object.keys(e.flash).length).toBe(IMAGE.length);
  for (let i = 0; i < IMAGE.length; i++) if (e.flash[i] !== IMAGE[i]) throw new Error(`byte 0x${i.toString(16)} differs`);
  expect(state.images).toBe(1);

  // The Board log: esptool's lines (no "Writing at" spam), then the console, a row a line,
  // colour codes and carriage returns gone.
  const log = u.locator("#usb-log");
  await log.locator("summary").click();
  const rows = u.locator("#usb-log-rows > div");
  await expect(rows.filter({ hasText: "I (311) espdns: espDNS 0.0.5 starting" })).toHaveCount(1);
  const texts = await rows.allTextContents();
  expect(texts).toContain("W (342) net: no link yet");
  expect(texts).toContain("ESP-ROM:esp32s3-20210327");
  expect(texts.filter(t => /\x1b|\r/.test(t))).toEqual([]);
  expect(texts.some(t => /^Writing at 0x/.test(t))).toBe(false);
  expect(texts.indexOf("--- the board's console ---")).toBeGreaterThan(0);
  // Download log: the same lines, as a text file.
  const [dl] = await Promise.all([page.waitForEvent("download"), u.locator("#usb-log-save").click()]);
  expect(dl.suggestedFilename()).toBe("espdns-ws-s3-eth-log.txt");
  const saved = fs.readFileSync(await dl.path(), "utf8");
  expect(saved).toContain("I (311) espdns: espDNS 0.0.5 starting\nW (342) net: no link yet\n");
  expect(saved).not.toMatch(/\x1b|\r/);
  // Restart the board: it boots again, its log once more.
  await u.locator("#usb-log-reset").click();
  await expect(rows.filter({ hasText: "I (311) espdns: espDNS 0.0.5 starting" })).toHaveCount(2);

  // Not waited for here: OK hands it to the controller, by its name, chip MAC, address and
  // the node key its console said; the panel closes, and Nodes has it waiting.
  expect(state.lookups).toEqual([]);
  // Refused (the name taken): said in the panel, which stays; another name is taken.
  state.refuse = "That name is in use (dns3.json says another network): choose another name";
  await u.getByRole("button", { name: "OK" }).click();
  await expect(u.locator("#usb-ok-error")).toHaveText(state.refuse);
  await expect(u).toBeVisible();
  state.refuse = "";
  await u.locator("#usb-name").fill("dns4");
  await u.getByRole("button", { name: "OK" }).click();
  await expect(u).toBeHidden();
  expect(state.waiting.map(({ since, state: st, ...w }) => w)).toEqual([{ name: "dns4", board: "ws-s3-eth", id: "aa:bb:cc:00:00:52", address: "192.0.2.52/24",
    gateway: "192.0.2.1", node_key: "BGsX0fLhLEJH+Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT+NC4v4af5uO5+tKfA+eFivOM1drMV7Oy7ZAaDe/UfU=" }]);
  const row = page.locator('#nodes [data-waiting="aa:bb:cc:00:00:52"]');
  await expect(row).toContainText("dns4");
  await expect(row).toContainText("Waiting for it: plug it into your network");
  await expect(row).toContainText("192.0.2.52");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// A board that is one of your nodes (its chip MAC a listed node's ID), installed from Nodes:
// OK hands it to the controller as a reinstall, by its ID, board, name and node key (the name
// typed is not its: it keeps its own), and Nodes has it waiting to get its settings back.
test("Install over USB: one of your nodes installed again, OK hands it over as a reinstall, waiting on Nodes", async ({ page }) => {
  await page.route(/\/api\/nodes$/, r => r.fulfill({ json: [{ id: "aa:bb:cc:00:00:52", addr: "192.0.2.52", hostname: "espdns-000052.local", source: "settings",
    online: false, status: { node_id: "aa:bb:cc:00:00:52", board: "ws-s3-eth", config: { name: "dns2", source: "node", seq: 7 } } }] }));
  const { u, state, violations } = await start(page);
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-installed")).toHaveText("Installed. Plug it into your network: it gets its settings back as soon as it answers.", { timeout: 20000 });
  await u.getByRole("button", { name: "OK" }).click();
  await expect(u).toBeHidden();
  expect(state.waiting.map(({ since, state: st, ...w }) => w)).toEqual([{ reinstall: true, id: "aa:bb:cc:00:00:52", board: "ws-s3-eth", name: "dns2",
    node_key: "BGsX0fLhLEJH+Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT+NC4v4af5uO5+tKfA+eFivOM1drMV7Oy7ZAaDe/UfU=" }]);
  expect(state.lookups).toEqual([]);
  const row = page.locator('#nodes [data-waiting="aa:bb:cc:00:00:52"]');
  await expect(row.locator(".flag")).toHaveText("Waiting for it: plug it into your network");
  await expect(row).toContainText("dns2");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("Install over USB: Erase the whole flash first erases, then writes; a USB-to-serial port says logs need the board's own USB", async ({ page }) => {
  const { u, violations } = await start(page, { fake: { vid: 0x10c4 } });
  await u.locator("#usb-erase").check();
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-installed")).toBeVisible({ timeout: 20000 });
  for (const n of ["connect", "chip", "erase", "write", "restart"]) await expect(step(u, n)).toHaveClass("done");
  await expect(step(u, "connect")).toContainText("Over a USB-to-serial chip");
  const e = await esp(page);
  expect(e.ops).toContain(0xd0);
  expect(e.ops.indexOf(0xd0)).toBeLessThan(e.ops.indexOf(0x10));
  expect(e.begins).toEqual([{ offset: 0, size: IMAGE.length }]);
  await expect(u.locator("#usb-uart")).toHaveText("Logs and Wi-Fi setup use the board's own USB port; this one only shows the bootloader.");
  // Install again: the console let go of, the bootloader reached again, written once more.
  await u.locator("#usb-erase").uncheck();
  await u.getByRole("button", { name: "Install again" }).click();
  await expect(step(u, "erase")).toHaveCount(0);
  await expect(u.locator("#usb-installed")).toBeVisible({ timeout: 20000 });
  await expect(step(u, "restart")).toHaveClass("done");
  expect((await esp(page)).begins).toEqual([{ offset: 0, size: IMAGE.length }, { offset: 0, size: IMAGE.length }]);
  expect(await violations()).toEqual([]);
});

test("Install over USB: an ESP32-P4 on the port and an ESP32-S3 image: Board Mismatch, nothing written", async ({ page }) => {
  const { u, violations } = await start(page, { fake: { magic: 0x0addbad0 } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-failed")).toHaveText("Board Mismatch ESP32-P4 in usb, firmware for ESP32-S3", { timeout: 20000 });
  await expect(step(u, "chip")).toHaveClass("failed");
  await expect(step(u, "write")).toHaveClass("");
  await expect(u.locator("#usb-after")).toContainText("Nothing was written.");
  await expect(u.locator("#usb-log")).toHaveAttribute("open", "");
  const e = await esp(page);
  expect(e.begins).toEqual([]);
  expect(e.ops).not.toContain(0xd0);
  await expect(u.locator("#usb-go")).toBeEnabled();
  await expect(u.locator("#usb-prepare")).toBeEnabled();
  expect(await violations()).toEqual([]);
});

// The two ESP32-P4 images run on different silicon (esp32p4-rev1: v1.x; esp32p4: v3.x): the
// image's bootloader header says which, and a chip outside it is refused like another chip.
// The image here is esp32p4-rev1's (v1.0 to v1.99, its bootloader at 0x2000).
const P4REV1 = Buffer.from(IMAGE);
P4REV1.set([0xe9], 0x2000);
P4REV1.writeUInt16LE(100, 0x2000 + 15);
P4REV1.writeUInt16LE(199, 0x2000 + 17);
const P4_REV_REG = 0x5012d000 + 68 + 8; // EFUSE_BLOCK1_ADDR + 8: minor in bits 0-3, major in 4-5
for (const [rev, reg, ok] of [["v3.0", 0x30, false], ["v1.3", 0x13, true]]) {
  test(`Install over USB: an ESP32-P4 ${rev} and the esp32p4-rev1 image: ${ok ? "written" : "Board Mismatch with the revisions, nothing written"}`, async ({ page }) => {
    const { u, violations } = await start(page, { board: "p4-ip101", fake: { magic: 0x0addbad0, regs: { [P4_REV_REG]: reg } } });
    await page.route(/\/build\/e2e-p4-ip101\/image\.bin$/, r => r.fulfill({ status: 200, contentType: "application/octet-stream", body: P4REV1 }));
    await u.locator("#usb-go").click();
    if (ok) {
      await expect(step(u, "write")).toHaveClass("done", { timeout: 20000 });
      expect((await esp(page)).begins).toEqual([{ offset: 0, size: P4REV1.length }]);
    } else {
      await expect(u.locator("#usb-failed")).toHaveText("Board Mismatch ESP32-P4 v3.0 in usb, firmware for ESP32-P4 v1.0 to v1.99", { timeout: 20000 });
      await expect(step(u, "chip")).toHaveClass("failed");
      expect((await esp(page)).begins).toEqual([]);
    }
    expect(await violations()).toEqual([]);
  });
}

// Write keys only after an install: the install's console lets go of the port first, then
// the keys are written; the port is never opened twice at once (the fake refuses that).
test("Install over USB, then Write keys only on the same port: the console let go of, the keys written", async ({ page }) => {
  const { u, violations } = await start(page);
  await page.route(/\/api\/build\/keys$/, r => r.fulfill({ json: { id: "e2e-keys", keys: { release: RELFP, recovery: RECFP } } }));
  await page.route(/\/build\/e2e-keys\/image\.bin$/, r => r.fulfill({ status: 200, contentType: "application/octet-stream", body: Buffer.alloc(0x2000, 0x4b) }));
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-installed")).toBeVisible({ timeout: 20000 });
  await expect(u.locator("#usb-log-reset")).toBeEnabled(); // the console is open
  await u.locator("#usb-keys-only summary").click();
  await u.getByRole("button", { name: "Write the keys over USB" }).click();
  await expect(u.locator("#usb-keys-status")).toContainText("Done: the board has your keys", { timeout: 20000 });
  expect((await esp(page)).begins).toEqual([{ offset: 0, size: IMAGE.length }, { offset: 0x1b000, size: 0x2000 }]);
  await expect(u.locator("#usb-go")).toBeEnabled();
  expect(await violations()).toEqual([]);
});

test("Install over USB: the port picker cancelled, nothing fetched or written", async ({ page }) => {
  const { u, state, violations } = await start(page);
  await page.evaluate(() => {
    const serial = { async requestPort() { throw new DOMException("No port selected by the user.", "NotFoundError"); }, async getPorts() { return []; },
      addEventListener() {}, removeEventListener() {} };
    Object.defineProperty(Navigator.prototype, "serial", { configurable: true, get: () => serial });
  });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-failed")).toHaveText("No port was chosen: nothing was written.");
  await expect(u.locator("#usb-steps")).toBeHidden();
  expect(state.images).toBe(0);
  await expect(u.locator("#usb-go")).toBeEnabled();
  expect(await violations()).toEqual([]);
});

test("Install over USB: a board not in its bootloader says to hold BOOT", async ({ page }) => {
  test.setTimeout(90000);
  const { u, violations } = await start(page, { fake: { syncs: 0 } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-failed")).toContainText("hold BOOT while you plug it back in", { timeout: 60000 });
  await expect(step(u, "connect")).toHaveClass("failed");
  expect((await esp(page)).begins).toEqual([]);
  await expect(u.locator("#usb-go")).toBeEnabled();
  expect(await violations()).toEqual([]);
});

test("Install over USB: a write that fails says so, Install stays", async ({ page }) => {
  const { u, violations } = await start(page, { fake: { failWrite: true } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-failed")).toContainText("Writing failed (", { timeout: 20000 });
  await expect(step(u, "write")).toHaveClass("failed");
  await expect(u.locator("#usb-go")).toHaveText("Install on the board over USB");
  await expect(u.locator("#usb-go")).toBeEnabled();
  // Not busy any more: the panel closes.
  await page.keyboard.press("Escape");
  await expect(u).toBeHidden();
  expect(await violations()).toEqual([]);
});

test("Install over USB: while it writes, the panel won't close, and says why in red", async ({ page }) => {
  const { u, violations } = await start(page, { fake: { hold: true } });
  await u.locator("#usb-go").click();
  await page.waitForFunction(() => window.__esp.held === true, null, { timeout: 20000 });
  await expect(step(u, "write")).toHaveClass("now");
  await expect(u.locator("#usb-board")).toBeDisabled();
  await expect(u.locator("#usb-prepare")).toBeDisabled();
  const busy = u.locator("#panel-busy");
  await expect(busy).toHaveCount(0);
  await u.getByRole("button", { name: "Close" }).last().click();
  await expect(busy).toHaveText("Writing to the board: don't close this or unplug it until it's done.");
  await expect(busy).toHaveClass(/\bbad\b/);
  if (process.env.E2E_SHOTS) await page.screenshot({ path: path.join(process.env.E2E_SHOTS, "install-busy.png") });
  await page.keyboard.press("Escape");
  await u.locator("header .x").click();
  await page.mouse.click(10, 300); // outside, on the scrim
  await expect(u).toBeVisible();
  // Leaving the page asks first; afterwards it doesn't.
  const leaving = () => page.evaluate(() => { const e = new Event("beforeunload", { cancelable: true }); window.dispatchEvent(e); return e.defaultPrevented; });
  expect(await leaving()).toBe(true);
  await page.evaluate(() => window.__esp.release());
  await expect(u.locator("#usb-installed")).toBeVisible({ timeout: 20000 });
  await expect(busy).toHaveCount(0);
  expect(await leaving()).toBe(false);
  await u.getByRole("button", { name: "Close" }).last().click();
  await expect(u).toBeHidden();
  expect(await violations()).toEqual([]);
});

test("Install over USB: a Wi-Fi board asks for its network, joins it; on DHCP, OK says Nodes shows it to reserve its address", async ({ page }) => {
  const { u, state, violations } = await start(page, { board: "xiao-s3-sense", address: "dhcp", fake: { wifi: true } });
  await expect(u.locator("#usb-wifi")).toBeVisible();
  await u.locator("#usb-go").click();
  await expect(step(u, "wifi")).toHaveClass("next", { timeout: 20000 });
  await expect(step(u, "wifi")).toContainText("espDNS 0.0.5 on esp32s3");
  await expect(u.locator("#usb-wifi-said")).toHaveText("2 networks found: pick one.");
  // A select, strongest first, then "Another network…": no text box with a list for a
  // password manager to take for a login.
  await expect(u.locator("#usb-ssid-pick option")).toHaveText(["example-net · -48 dBm · \u{1F512}", "example-guest · -71 dBm · open", "Another network…"]);
  await expect(u.locator("#usb-wifi-form datalist")).toHaveCount(0);
  await expect(u.locator("#usb-ssid")).toBeHidden();
  await expect(u.locator("#usb-pass")).toHaveAttribute("autocomplete", "new-password");
  await expect(u.locator("#usb-ssid-pick")).toHaveValue("example-net");
  await u.locator("#usb-ssid-pick").selectOption("example-net");
  await u.locator("#usb-pass").fill("e2e password");
  if (process.env.E2E_SHOTS) {
    await u.locator("#usb-log summary").click();
    await u.locator("#usb-join").scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(process.env.E2E_SHOTS, "install-wifi.png") });
  }
  await u.locator("#usb-join").click();
  await expect(step(u, "wifi")).toHaveClass("done");
  await expect(step(u, "wifi")).toContainText("joined example-net");
  await expect(u.locator("#usb-wifi-at")).toHaveText("On your Wi-Fi at 192.0.2.52");
  // The network and the password, as the firmware's RPC reads them.
  expect((await esp(page)).join).toBe("\x0bexample-net\x0ce2e password");
  await expect(u.locator("#usb-installed")).toHaveText("Installed, and on your Wi-Fi network: Nodes shows it once it answers, to reserve its address and add it.");
  await expect(u.locator("#usb-after")).not.toContainText("cable");
  await expect(step(u, "net")).not.toContainText("cable");
  await u.getByRole("button", { name: "OK" }).click();
  await expect(u).toBeHidden();
  expect(state.waiting.map(w => [w.name, w.address, w.gateway])).toEqual([["dns3", "dhcp", undefined]]);
  await expect(page.locator('#nodes [data-waiting="aa:bb:cc:00:00:52"]')).toContainText("DHCP");
  expect(state.lookups).toEqual([]);
  // No Improv frame in the log, only the console's lines.
  expect((await u.locator("#usb-log-rows > div").allTextContents()).some(t => t.includes("IMPROV"))).toBe(false);
  expect(await violations()).toEqual([]);
});

test("Install over USB: a Wi-Fi join refused says to check the password, and can be tried again", async ({ page }) => {
  const { u, violations } = await start(page, { board: "xiao-s3-sense", fake: { wifi: true, join: false } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-wifi-said")).toHaveText("2 networks found: pick one.", { timeout: 20000 });
  await u.locator("#usb-ssid-pick").selectOption("example-guest");
  await u.locator("#usb-pass").fill("not it");
  await u.locator("#usb-join").click();
  await expect(u.locator("#usb-wifi-said")).toHaveText("Couldn't join: check the password");
  await expect(u.locator("#usb-wifi-said")).toHaveClass(/\bbad\b/);
  await expect(u.locator("#usb-join")).toBeEnabled();
  await expect(step(u, "wifi")).toHaveClass("next");
  await expect(u.locator("#usb-installed")).toHaveCount(0);
  expect(await violations()).toEqual([]);
});

test("Install over USB: Another network… joins a network typed by name", async ({ page }) => {
  const { u, violations } = await start(page, { board: "xiao-s3-sense", fake: { wifi: true } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-wifi-said")).toHaveText("2 networks found: pick one.", { timeout: 20000 });
  await u.locator("#usb-ssid-pick").selectOption({ label: "Another network…" });
  await expect(u.locator("#usb-ssid")).toBeVisible();
  await expect(u.locator("#usb-ssid")).toBeFocused();
  await expect(u.locator("#usb-ssid")).toHaveAttribute("autocomplete", "off");
  await u.locator("#usb-ssid").fill("  hidden-net ");
  await u.locator("#usb-pass").fill("e2e password");
  await u.locator("#usb-join").click();
  await expect(step(u, "wifi")).toHaveClass("done");
  await expect(step(u, "wifi")).toContainText("joined hidden-net");
  expect((await esp(page)).join).toBe("\x0ahidden-net\x0ce2e password");
  expect(await violations()).toEqual([]);
});

test("Install over USB: a Wi-Fi board that never answers Improv nor logs: it uses Ethernet, press RESET", async ({ page }) => {
  test.setTimeout(60000);
  const { u, violations } = await start(page, { board: "xiao-s3-sense", fake: { bootLog: [] } });
  await u.locator("#usb-go").click();
  await expect(step(u, "wifi")).toHaveClass("now", { timeout: 20000 });
  await expect(u.locator("#usb-nolog")).toHaveText("If it shows nothing, press RESET or unplug it and plug it in again.", { timeout: 15000 });
  await expect(step(u, "wifi")).toContainText("It didn't ask for Wi-Fi: it uses its Ethernet add-on.", { timeout: 20000 });
  // No node key line either: restarted once to read it, then OK all the same.
  await expect(u.locator("#usb-installed")).toHaveText("Installed. Plug it into your network: it's added to your nodes as soon as it answers.", { timeout: 20000 });
  await expect(step(u, "net")).toContainText("Plug its network cable in.");
  expect(await violations()).toEqual([]);
});

// The node key line missed at the first boot (said before the console was open again): the
// board is restarted once through its console, and the key it says then goes with OK.
test("Install over USB: a node key line missed is read after one restart", async ({ page }) => {
  const { u, state, violations } = await start(page, { fake: { keyLate: true } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-ok")).toBeVisible({ timeout: 25000 });
  const rows = await u.locator("#usb-log-rows > div").allTextContents();
  expect(rows).toContain("--- restarting the board to read its node key ---");
  expect(rows.filter(t => t.includes("espDNS 0.0.5 starting"))).toHaveLength(2);
  await u.getByRole("button", { name: "OK" }).click();
  await expect(u).toBeHidden();
  expect(state.waiting[0].node_key).toBe("BGsX0fLhLEJH+Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT+NC4v4af5uO5+tKfA+eFivOM1drMV7Oy7ZAaDe/UfU=");
  expect(await violations()).toEqual([]);
});

// A chip MAC not read: no OK (the controller couldn't tell the board), the panel waits for it
// as + Add node's does.
test("Install over USB: a board whose chip MAC isn't read is waited for in the panel", async ({ page }) => {
  const { u, state, violations } = await start(page, { fake: { mac: "00:00:00:00:00:00" } });
  await u.locator("#usb-go").click();
  await expect(u.locator("#usb-waiting")).toHaveText("Waiting for it at 192.0.2.52…", { timeout: 20000 });
  await expect(u.locator("#usb-ok")).toHaveCount(0);
  await expect.poll(() => state.lookups.length).toBeGreaterThan(0);
  expect(state.waiting).toEqual([]);
  expect(await violations()).toEqual([]);
});
