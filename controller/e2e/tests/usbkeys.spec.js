// Install over USB and the keys (usb.js, builder.go): a board is installed with your keys, so
// without them nothing is prepared (the page says so, and serve.sh's controller, which has
// none, refuses); Write keys only (keeps settings) writes the keys partition alone with the
// vendored esptool-js, on a stubbed Web Serial port with a fake ESP32-S3 bootloader behind
// it: no hardware, no ESP Web Tools dialog, no erase. A node found in setup mode offers it
// first. The keys and the prepared build are answered here (page.route), so serve.sh's
// controller keeps no keys for the other tests.
const { test, expect } = require("@playwright/test");
const { login } = require("./session");
const { fakeEsp } = require("./fakeboard");

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

const RELFP = "a1b2c3d4e5f60718", RECFP = "0f1e2d3c4b5a6978";
// The keys partition the fake controller hands out: 8 KB, every byte telling where it is.
const KEYS_BIN = Buffer.from(Array.from({ length: 0x2000 }, (_, i) => (i * 7 + (i >> 8)) & 0xff));

// keys answers GET /api/key with both keys, and Write keys only's build.
async function keys(page) {
  const json = (r, b, s = 200) => r.fulfill({ status: s, contentType: "application/json", body: JSON.stringify(b) });
  await page.route(/\/api\/key$/, r => json(r, { present: true, fingerprint: RELFP, source: "file", recovery: { present: true, fingerprint: RECFP }, nodes_listed: false }));
  await page.route(/\/api\/build\/keys$/, r => json(r, { id: "keys-e2e", keys: { release: RELFP, recovery: RECFP }, keys_only: true, board: {}, chip: {} }));
  await page.route(/\/build\/keys-e2e\/image\.bin$/, r => r.fulfill({ status: 200, contentType: "application/octet-stream", body: KEYS_BIN }));
}

async function openUSB(page) {
  await page.goto("./");
  await page.getByRole("button", { name: "Install over USB", exact: true }).click();
  const d = page.getByRole("dialog", { name: "Install a board over USB" });
  await expect(d).toBeVisible();
  return d;
}

test("Install over USB without keys: nothing prepared, and the controller refuses", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const d = await openUSB(page);
  await expect(d.locator("#usb-nokeys")).toContainText("Set up your keys first");
  await expect(d.locator("#usb-nokeys a")).toHaveAttribute("href", "setup.html#next");
  await d.locator("#usb-board").selectOption("ws-s3-eth");
  await expect(d.locator("#usb-prepare")).toBeDisabled();
  await expect(d.locator("#usb-keys-go")).toBeDisabled();
  // serve.sh's controller has no keys: both builds refused, saying so.
  const res = await page.evaluate(async () => {
    const tok = localStorage.getItem("espdns.session") || "";
    const post = async (url, body) => {
      const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json", "X-Session-Token": tok }, body: JSON.stringify(body) });
      return [r.status, await r.text()];
    };
    return [await post("api/build", { name: "ws-s3-eth", network: { address: "192.0.2.60/24", gateway: "192.0.2.1" } }), await post("api/build/keys", {})];
  });
  for (const [status, text] of res) {
    expect(status).toBe(422);
    expect(text).toContain("set up your keys first");
  }
  expect(await violations()).toEqual([]);
});

// The board library's search box: typing filters the list (maker, model, catalog name, chip
// image, w5500…) keeping the by-maker groups; the custom-board entry stays; a filtered
// board picked fills its facts; each option says its flash, PSRAM and wired or Wi-Fi.
test("Install over USB: the search box filters the library, and a filtered board fills its facts", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const d = await openUSB(page);
  const opts = d.locator("#usb-board option"), groups = d.locator("#usb-board optgroup");
  const all = await opts.count();
  expect(all).toBeGreaterThan(20);
  expect(await groups.count()).toBeGreaterThan(5);
  await expect(d.locator('#usb-board option[value="lilygo-t-eth-lite-s3"]')).toHaveText(/LilyGO T-ETH-Lite ESP32-S3 · 16 MB flash, 8 MB PSRAM · wired · community/);

  await d.locator("#usb-q").fill("waveshare");
  await expect(groups).toHaveCount(1);
  await expect(groups).toHaveAttribute("label", "Waveshare");
  const n = await opts.count();
  expect(n).toBeLessThan(all);
  for (const text of await opts.allTextContents()) expect(text).toMatch(/^Waveshare|^Choose…|^Describe your own board…/);

  // Two words: both must match (chip image and maker), the board's facts fill when picked.
  await d.locator("#usb-q").fill("lilygo esp32s3-eth");
  await expect(groups).toHaveCount(1);
  await d.locator("#usb-board").selectOption("lilygo-t-eth-lite-s3");
  await expect(d.locator("#usb-facts")).toContainText("esp32s3-eth");
  await expect(d.locator("#usb-facts")).toContainText("16 MB, 8 MB PSRAM");
  await expect(d.locator("#usb-facts")).toContainText("Wired (W5500)");

  // Nothing matches: the list says so, the chosen board stays, and the custom entry is there.
  await d.locator("#usb-q").fill("zzzz");
  await expect(opts.first()).toHaveText("No board matches");
  await expect(d.locator("#usb-board")).toHaveValue("lilygo-t-eth-lite-s3");
  await expect(d.locator('#usb-board option[value="+"]')).toBeAttached();
  await d.locator("#usb-q").fill("");
  await expect(opts).toHaveCount(all);
  expect(await violations()).toEqual([]);
});

// Write keys only: the keys partition written by the page itself with the vendored esptool-js
// on the board's bootloader (here a fake one on a stubbed port): one compressed write of 8 KB
// at 0x1b000, read back, the board reset (RTS raised, then dropped); no erase command at all (ERASE_FLASH 0xd0,
// ERASE_REGION 0xd1), no uncompressed FLASH_BEGIN, nothing written outside the keys region,
// and no ESP Web Tools dialog.
test("Write keys only: esptool-js writes the 8 KB at 0x1b000 alone, erases nothing, reads it back", async ({ page }) => {
  const violations = await watch(page);
  await page.addInitScript(fakeEsp);
  await login(page);
  await keys(page);
  const d = await openUSB(page);
  await expect(d.locator("#usb-nokeys")).toHaveCount(0);
  // Under Advanced, closed for a new board; no erase offered anywhere in it.
  const only = d.locator("#usb-keys-only");
  await expect(only).not.toHaveAttribute("open", "");
  await only.locator("summary").click();
  await expect(only).not.toContainText(/erase device/i);
  await only.getByRole("button", { name: "Write the keys over USB" }).click();
  await expect(only.locator("#usb-keys-status")).toContainText("Done: the board has your keys", { timeout: 20000 });
  await expect(only.locator("#usb-keys-status")).toContainText(`release ${RELFP}, recovery ${RECFP}`);
  await expect(page.locator("ewt-install-dialog")).toHaveCount(0);
  const esp = await page.evaluate(() => ({ ops: window.__esp.ops, begins: window.__esp.begins, flash: window.__esp.flash, signals: window.__esp.signals }));
  expect(esp.ops).not.toContain(0xd0);
  expect(esp.ops).not.toContain(0xd1);
  expect(esp.ops).not.toContain(0x02);
  expect(esp.begins).toEqual([{ offset: 0x1b000, size: 0x2000 }]);
  expect(esp.ops).toContain(0x06); // the flasher stub uploaded and run
  expect(esp.ops).toContain(0xd2); // read back
  const written = Object.keys(esp.flash).map(Number);
  expect(written.length).toBe(0x2000);
  expect(Math.min(...written)).toBe(0x1b000);
  expect(Math.max(...written)).toBe(0x1cfff);
  // The bytes are the keys partition the controller made.
  for (let i = 0; i < KEYS_BIN.length; i++) if (esp.flash[0x1b000 + i] !== KEYS_BIN[i]) throw new Error(`byte 0x${(0x1b000 + i).toString(16)} differs`);
  expect(esp.signals.length).toBeGreaterThan(0); // reset into the bootloader and back out
  // The restart after the last command: RTS raised, then dropped (esptool-js only drops it).
  const rts = esp.signals.filter(s => s.after === esp.ops.length && "requestToSend" in s).map(s => s.requestToSend);
  expect(rts).toEqual([true, false]);
  expect(await violations()).toEqual([]);
});

// The port picker cancelled: nothing written, said so.
test("Write keys only: no port chosen, nothing written", async ({ page }) => {
  const violations = await watch(page);
  await page.addInitScript(() => {
    const serial = { async requestPort() { throw new DOMException("No port selected by the user.", "NotFoundError"); }, async getPorts() { return []; },
      addEventListener() {}, removeEventListener() {} };
    Object.defineProperty(Navigator.prototype, "serial", { configurable: true, get: () => serial });
  });
  await login(page);
  await keys(page);
  let built = 0;
  page.on("request", r => { if (/\/api\/build\/keys$/.test(r.url())) built++; });
  const d = await openUSB(page);
  await d.locator("#usb-keys-only summary").click();
  await d.getByRole("button", { name: "Write the keys over USB" }).click();
  await expect(d.locator("#usb-keys-status")).toHaveText("No port was chosen: nothing was written.");
  expect(built).toBe(0);
  expect(await violations()).toEqual([]);
});

test("a node found with no keys: Write keys over USB, the panel opening on it", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await keys(page);
  await page.route(/\/api\/nodes\/found$/, r => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ nodes: [{
    host: "198.51.100.39", id: "aa:bb:cc:00:00:39", hostname: "espdns-000039.local", source: "mdns", online: true, read: true, board: "ws-s3-eth",
    image: "esp32s3-octal", version: "0.0.5", net: "ethernet", adopted: false, listed: false, configs: true, next: "", install: "keys", keys_source: "none",
    refusals: ["it has no keys (setup mode): write them over USB first (Install over USB, Write keys only)"] }] }) }));
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  await expect(page.locator("#nqres")).toContainText("No keys (setup mode): write them over USB first");
  await page.locator("#nqres").getByRole("button", { name: "Write keys over USB" }).click();
  const d = page.getByRole("dialog", { name: /^Write keys to .* over USB$/ });
  await expect(d).toBeVisible();
  await expect(d.locator("#usb-keys-only")).toHaveAttribute("open", "");
  await expect(d.locator("#usb-keys-only")).toContainText("This board has no keys");
  expect(await violations()).toEqual([]);
});
