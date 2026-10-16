// Install over USB and the keys (usb.js, builder.go): a board is installed with your keys, so
// without them nothing is prepared (the page says so, and serve.sh's controller, which has
// none, refuses); Write keys only (keeps settings) writes the keys partition alone with the
// vendored esptool-js, on a stubbed Web Serial port with a fake ESP32-S3 bootloader behind
// it: no hardware, no ESP Web Tools dialog, no erase. A node found in setup mode offers it
// first. The keys and the prepared build are answered here (page.route), so serve.sh's
// controller keeps no keys for the other tests.
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

// fakeEsp is a Web Serial port with an ESP32-S3's serial bootloader behind it, as far as
// esptool-js speaks to it: SLIP frames; SYNC answered (the ROM's non-zero value); registers
// read as 0, but the chip-detect magic (0x9, the S3); the flasher stub's upload taken and
// its "OHAI" sent; compressed flash writes (FLASH_DEFL_BEGIN/DATA/END) inflated into a
// sparse flash; READ_FLASH answered from it. Every command's op is kept in window.__esp.ops,
// each write's offset and size in .begins, the flash written in .flash (offset -> byte).
function fakeEsp() {
  const esp = { ops: [], begins: [], flash: {}, opened: 0, signals: 0 };
  window.__esp = esp;
  let ctrl = null;
  const readable = new ReadableStream({ start(c) { ctrl = c; } });
  const slip = data => {
    const out = [0xc0];
    for (const b of data) {
      if (b === 0xc0) out.push(0xdb, 0xdc); else if (b === 0xdb) out.push(0xdb, 0xdd); else out.push(b);
    }
    out.push(0xc0);
    return new Uint8Array(out);
  };
  const send = data => ctrl.enqueue(slip(data));
  const u32 = (b, o) => (b[o] | b[o + 1] << 8 | b[o + 2] << 16 | b[o + 3] << 24) >>> 0;
  const reply = (op, val = 0, data = [0, 0, 0, 0]) => {
    const p = new Uint8Array(8 + data.length);
    p[0] = 1; p[1] = op; p[2] = data.length & 0xff; p[3] = data.length >> 8;
    p[4] = val & 0xff; p[5] = val >> 8 & 0xff; p[6] = val >> 16 & 0xff; p[7] = val >>> 24;
    p.set(data, 8);
    send(p);
  };
  let defl = null;
  async function command(pkt) {
    if (pkt.length < 8 || pkt[0] !== 0) return; // not a command: read_flash's acks
    const op = pkt[1], data = pkt.slice(8);
    esp.ops.push(op);
    switch (op) {
      case 0x08: for (let i = 0; i < 8; i++) reply(op, 0x20120707); return; // SYNC
      case 0x0a: reply(op, u32(data, 0) === 0x40001000 ? 0x9 : 0); return; // READ_REG
      case 0x06: reply(op); send(new TextEncoder().encode("OHAI")); return; // MEM_END: the stub runs
      case 0x10: // FLASH_DEFL_BEGIN: uncompressed size, blocks, block size, offset
        defl = { size: u32(data, 0), offset: u32(data, 12), parts: [] };
        esp.begins.push({ offset: defl.offset, size: defl.size });
        reply(op); return;
      case 0x11: defl.parts.push(data.slice(16, 16 + u32(data, 0))); reply(op); return; // FLASH_DEFL_DATA
      case 0x12: { // FLASH_DEFL_END: inflate (zlib) and write
        const blob = new Blob(defl.parts).stream().pipeThrough(new DecompressionStream("deflate"));
        const raw = new Uint8Array(await new Response(blob).arrayBuffer());
        raw.forEach((b, i) => { esp.flash[defl.offset + i] = b; });
        reply(op); return;
      }
      case 0xd2: { // READ_FLASH (the stub's): offset, length, block size; then the data
        const off = u32(data, 0), len = u32(data, 4), bs = u32(data, 8);
        reply(op, 0, [0, 0]);
        for (let p = 0; p < len; p += bs) {
          const chunk = new Uint8Array(Math.min(bs, len - p));
          for (let i = 0; i < chunk.length; i++) chunk[i] = esp.flash[off + p + i] ?? 0xff;
          send(chunk);
        }
        return;
      }
    }
    reply(op); // anything else: done (WRITE_REG, MEM_BEGIN/DATA, SPI_ATTACH, ...)
  }
  // The host's bytes, as SLIP frames, one command at a time.
  let frame = null, esc = false, queue = Promise.resolve();
  const writable = new WritableStream({
    write(chunk) {
      for (const b of chunk) {
        if (b === 0xc0) {
          if (frame && frame.length) { const f = new Uint8Array(frame); queue = queue.then(() => command(f)); }
          frame = [];
          continue;
        }
        if (!frame) continue;
        if (esc) { frame.push(b === 0xdc ? 0xc0 : b === 0xdd ? 0xdb : b); esc = false; } else if (b === 0xdb) esc = true; else frame.push(b);
      }
      return queue;
    },
  });
  const port = {
    readable, writable,
    async open() { esp.opened++; }, async close() {}, async setSignals() { esp.signals++; }, async forget() {},
    getInfo() { return { usbVendorId: 0x303a, usbProductId: 0x1001 }; },
    addEventListener() {}, removeEventListener() {},
  };
  const serial = { async requestPort() { return port; }, async getPorts() { return [port]; }, addEventListener() {}, removeEventListener() {} };
  Object.defineProperty(Navigator.prototype, "serial", { configurable: true, get: () => serial });
}

// Write keys only: the keys partition written by the page itself with the vendored esptool-js
// on the board's bootloader (here a fake one on a stubbed port): one compressed write of 8 KB
// at 0x1b000, read back, the board reset; no erase command at all (ERASE_FLASH 0xd0,
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
  expect(esp.signals).toBeGreaterThan(0); // reset into the bootloader and back out
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
