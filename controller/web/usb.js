// Install over USB (its own button on Nodes and in the first run, a found node's "Update
// over USB", and a node page's "Reinstall over USB"): a panel that installs espDNS on a board
// plugged into this computer, all inside the panel. The board from the catalogue, or one of
// yours, or one described from its pinout (the custom board form, POST /api/boards/check,
// saved with POST /api/boards); the node's address, which goes into its board partition;
// Prepare image (POST /api/build) makes /build/<id>/image.bin. Install on the board over USB
// asks for the port, then, with flash.js (the vendored esptool-js) at 115200: connects to the
// board's bootloader (saying whether it is the board's own USB or a USB-to-serial chip),
// checks the chip is the image's (else "Board Mismatch <found> in usb, firmware for <image's>",
// nothing written) and its flash is big enough, erases the whole flash if asked, writes the
// image at 0 and restarts the board. Closing the panel is refused while it erases or writes.
// Then the board's console is read into the Board log (boardlog.js), and on a board that may
// run on Wi-Fi (build.wifi: boards.Board.MayRunOnWiFi) the panel asks for the network over
// Improv in that same stream; the network and password go to the board over USB, never
// through the controller. The image carries your keys (the release and recovery public keys,
// in its keys partition): until both are set up (the first run's Make your keys) nothing is
// prepared. Write keys only (POST /api/build/keys) writes the keys partition alone, for a
// node in setup mode, and keeps the rest. A board whose chip image isn't in the controller yet
// offers Get firmware (firmware.js: that chip image from the project's release, fetched,
// checked and imported); each board says which firmware it gets ("project release v0.0.3",
// "your build"). Once installed, the panel waits for the node on the network (POST
// /api/nodes/lookup at its address, or /api/nodes/found for one on DHCP) and, when it
// answers, says so with Add it now, which hands it to o.found (+ Add node, with it chosen and
// its address filled in). A new board installed from Nodes or the first run (o.waiting) isn't
// waited for here: once installed, the panel says to plug it into the network, and its OK
// hands it to the controller by its name, chip MAC, address and node key (POST
// /api/nodes/waiting), which adds it once it answers (on DHCP, once its address is reserved:
// Add it on Nodes); o.waiting() is called then. On an ESP32-S3 the node key is read from its
// console's "node key" line, the board restarted once if it wasn't seen. One of your nodes
// installed again (its chip MAC a listed node's ID; from its page or not) is handed over the
// same way, as a reinstall (no name asked: it keeps its own): the controller sends it its
// settings back once it answers. A board whose chip MAC wasn't read is waited for as before.
import { h, arr, obj, str, panel, api, hostOnly, getJSON, post } from "./shell.js";
import { every } from "./common.js";
import { firmwareBox, readFirmware, labelOf } from "./firmware.js";
import * as flash from "./flash.js";
import { logView, openConsole, clean, IMPROV, rpc, joinData, result } from "./boardlog.js";

// The builder's routes answer errors as text (http.Error), the others as JSON.
async function send(url, body) {
  const r = await api(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const text = await r.text();
  if (!r.ok) {
    let msg = text.trim();
    try { msg = JSON.parse(text).error || msg; } catch { /* text */ }
    throw new Error(msg || r.statusText);
  }
  return JSON.parse(text);
}
async function read(url) {
  const r = await api(url);
  if (!r.ok) throw new Error(`${url}: ${(await r.text()).trim() || r.statusText}`);
  return r.json();
}

// held is what's left to do with a port an install or a keys write let go of (the panel
// closed, Install again): the next one, on this panel or another, waits for it first, so the
// port is closed before it is opened again. hold(p) adds p, which settles once its port is shut.
let held = Promise.resolve();
const hold = p => { held = Promise.all([held, Promise.resolve(p).catch(() => {})]); return p; };

// usb.css: loaded once, before the panel first opens.
let css = null;
function loadCSS() {
  if (!css) {
    css = new Promise(res => {
      const l = h("link", { rel: "stylesheet", href: "usb.css" });
      l.addEventListener("load", res);
      l.addEventListener("error", res);
      document.head.append(l);
    });
  }
  return css;
}

function summary(b) {
  const e = b.ethernet || { kind: "none" };
  const wired = e.kind === "emac" ? `${str(e.phy || "ip101").toUpperCase()} on the built-in MAC` : e.kind === "w5500" ? "W5500" : "";
  const net = !wired ? "Wi-Fi" : e.optional ? `Wi-Fi; wired instead if a ${wired} add-on is fitted` : `Wired (${wired})`;
  const sd = !b.sd || b.sd.kind === "none" ? "no SD" : b.sd.kind === "sdmmc" ? "SDMMC" : "SD over SPI";
  return `${net} · ${sd}`;
}

function problems(box, errors, warnings) {
  box.replaceChildren(...arr(errors).map(t => h("div", { class: "bad small" }, str(t))),
    ...arr(warnings).map(t => h("div", { class: "small warn-text" }, `! ${str(t)}`)));
}

// ---- the custom board form ----------------------------------------------------------------
// Each field as [name, label, control]; a field with "for" shows when its fieldset's kind
// (its first select) is one of those.
const num = v => (v === "" || v === undefined ? undefined : parseInt(v, 10));
const pin = (name, label, extra = {}) => [name, label, { type: "number", min: 0, max: 54, ...extra }];
const sel = (name, label, opts, extra = {}) => [name, label, { options: opts, ...extra }];
function sections(images) {
  return [
    [null, [["name", "Name", { placeholder: "my-board", maxlength: 23, required: true }], ["title", "Title", { placeholder: "Maker Model" }],
      sel("image", "Chip image", images), sel("flash_mb", "Flash, MB", [["4"], ["8"], ["16"], ["32"]], { value: "8" }),
      ["psram_mb", "PSRAM, MB", { type: "number", min: 0, max: 32 }]]],
    ["SPI buses", [sel("spi0_host", "Bus A", [["", "none"], ["2"], ["3"]]), pin("spi0_sclk", "SCLK"), pin("spi0_mosi", "MOSI"), pin("spi0_miso", "MISO"),
      sel("spi1_host", "Bus B", [["", "none"], ["2"], ["3"]]), pin("spi1_sclk", "SCLK"), pin("spi1_mosi", "MOSI"), pin("spi1_miso", "MISO")]],
    ["Ethernet", [sel("eth_kind", "Kind", [["none", "none (Wi-Fi)"], ["emac", "built-in MAC + PHY"], ["w5500", "W5500 over SPI"]]),
      sel("eth_phy", "PHY", [["ip101"], ["lan87xx"], ["rtl8201"], ["dp83848"]], { for: "emac" }),
      ["eth_addr", "PHY address", { type: "number", min: -1, max: 31, placeholder: "1", for: "emac" }],
      pin("eth_reset", "PHY reset", { for: "emac" }), pin("eth_power", "PHY power", { for: "emac" }),
      pin("eth_mdc", "MDC", { placeholder: "chip default", for: "emac" }), pin("eth_mdio", "MDIO", { placeholder: "chip default", for: "emac" }),
      sel("eth_clock", "RMII clock", [["", "chip default"], ["in", "in from a GPIO"], ["out", "out to a GPIO"]], { for: "emac" }),
      pin("eth_clock_gpio", "Clock GPIO", { for: "emac" }),
      sel("eth_spi", "SPI bus", [["2"], ["3"]], { for: "w5500" }), pin("eth_cs", "CS", { for: "w5500" }), pin("eth_int", "INT", { for: "w5500" }),
      pin("eth_rst", "RST", { for: "w5500" }), ["eth_mhz", "MHz", { type: "number", min: 1, max: 80, placeholder: "20", for: "w5500" }],
      ["eth_optional", "An add-on that may not be fitted", { type: "checkbox", for: "emac w5500" }]]],
    ["SD card", [sel("sd_kind", "Kind", [["none"], ["sdmmc", "SDMMC"], ["spi", "over SPI"]]),
      ["sd_slot", "Slot", { type: "number", min: 0, max: 1, placeholder: "0", for: "sdmmc" }], sel("sd_width", "Width", [["4"], ["1"]], { for: "sdmmc" }),
      ["sd_ldo", "LDO channel", { type: "number", min: 0, max: 4, placeholder: "none", for: "sdmmc" }],
      sel("sd_spi", "SPI bus", [["2"], ["3"]], { for: "spi" }), pin("sd_cs", "CS", { for: "spi" })]],
    ["LED and Wi-Fi", [sel("led_kind", "LED", [["none"], ["gpio", "on a GPIO"], ["ws2812", "WS2812 (RGB)"]]), pin("led_pin", "LED pin", { for: "gpio ws2812" }),
      ["led_low", "Active low", { type: "checkbox", for: "gpio" }],
      ["wifi_tx", "Wi-Fi transmit cap, dBm", { type: "number", min: 2, max: 20, placeholder: "20 (chip default)" }]]],
  ];
}

function field([name, label, o]) {
  const at = { name, id: `usb-${name}` };
  if (o.type === "checkbox") return h("label", { class: "check", "data-for": o.for || null }, h("input", { ...at, type: "checkbox" }), h("span", {}, label));
  let c;
  if (o.options) {
    c = h("select", { ...at, class: "t" }, o.options.map(([v, t]) => h("option", { value: v, selected: o.value === v }, t === undefined ? v : t)));
  } else {
    c = h("input", { ...at, class: "t", type: o.type || "text", min: o.min, max: o.max, maxlength: o.maxlength, placeholder: o.placeholder,
      required: o.required, spellcheck: "false", autocomplete: "off" });
  }
  return h("label", { class: "f", "data-for": o.for || null }, h("span", {}, label), c);
}

function formToBoard(f) {
  const v = n => (f.elements[n].type === "checkbox" ? f.elements[n].checked : f.elements[n].value);
  const b = { name: v("name").trim(), title: v("title").trim() || undefined, image: v("image"), flash_mb: num(v("flash_mb")),
    psram_mb: num(v("psram_mb")), tier: "untested" };
  const spi = [0, 1].filter(i => v(`spi${i}_host`)).map(i => ({ host: num(v(`spi${i}_host`)), sclk: num(v(`spi${i}_sclk`)), mosi: num(v(`spi${i}_mosi`)), miso: num(v(`spi${i}_miso`)) }));
  if (spi.length) b.spi = spi;
  const ek = v("eth_kind");
  if (ek === "emac") {
    b.ethernet = { kind: ek, phy: v("eth_phy"), addr: num(v("eth_addr")), reset: num(v("eth_reset")), power: num(v("eth_power")),
      mdc: num(v("eth_mdc")), mdio: num(v("eth_mdio")), optional: v("eth_optional") || undefined };
    if (v("eth_clock")) b.ethernet.rmii_clock = { mode: v("eth_clock"), gpio: num(v("eth_clock_gpio")) };
  } else if (ek === "w5500") {
    b.ethernet = { kind: ek, spi_host: num(v("eth_spi")), cs: num(v("eth_cs")), int: num(v("eth_int")), rst: num(v("eth_rst")),
      mhz: num(v("eth_mhz")), optional: v("eth_optional") || undefined };
  }
  const sk = v("sd_kind");
  if (sk === "sdmmc") b.sd = { kind: sk, slot: num(v("sd_slot")), width: num(v("sd_width")), ldo: num(v("sd_ldo")) };
  else if (sk === "spi") b.sd = { kind: sk, spi_host: num(v("sd_spi")), cs: num(v("sd_cs")) };
  const lk = v("led_kind");
  if (lk !== "none") b.led = { kind: lk, pin: num(v("led_pin")), active_low: (lk === "gpio" && v("led_low")) || undefined };
  if (v("wifi_tx")) b.wifi = { tx_power_dbm: num(v("wifi_tx")) };
  return JSON.parse(JSON.stringify(b)); // drops undefined
}

function boardToForm(f, b) {
  const set = (n, val) => {
    if (val === undefined || val === null) return;
    const e = f.elements[n];
    if (e.type === "checkbox") e.checked = !!val; else e.value = val;
  };
  set("name", b.name); set("title", b.title); set("image", b.image); set("flash_mb", b.flash_mb); set("psram_mb", b.psram_mb);
  arr(b.spi).forEach((s, i) => { set(`spi${i}_host`, s.host); set(`spi${i}_sclk`, s.sclk); set(`spi${i}_mosi`, s.mosi); set(`spi${i}_miso`, s.miso); });
  const e = b.ethernet || { kind: "none" };
  set("eth_kind", e.kind); set("eth_phy", e.phy); set("eth_addr", e.addr); set("eth_reset", e.reset); set("eth_power", e.power);
  set("eth_mdc", e.mdc); set("eth_mdio", e.mdio); set("eth_spi", e.spi_host); set("eth_cs", e.cs); set("eth_int", e.int);
  set("eth_rst", e.rst); set("eth_mhz", e.mhz); set("eth_optional", e.optional);
  if (e.rmii_clock) { set("eth_clock", e.rmii_clock.mode); set("eth_clock_gpio", e.rmii_clock.gpio); }
  const s = b.sd || { kind: "none" };
  set("sd_kind", s.kind); set("sd_slot", s.slot); set("sd_width", s.width); set("sd_ldo", s.ldo); set("sd_spi", s.spi_host); set("sd_cs", s.cs);
  const l = b.led || { kind: "none" };
  set("led_kind", l.kind); set("led_pin", l.pin); set("led_low", l.active_low);
  if (b.wifi) set("wifi_tx", b.wifi.tx_power_dbm);
}

// ---- Write keys only ------------------------------------------------------------------------
// The controller's two public keys written to the board's keys partition and nothing else, for
// a node in setup mode (no keys, or corrupt ones): POST /api/build/keys makes the 8 KB
// partition image (/build/<id>/image.bin); flash.js (the vendored esptool-js) connects to the
// board's bootloader, writes it at 0x1b000 with no erase of anything else (esptool never
// sends an erase command; the flasher stub rewrites only the two sectors it is given), reads
// it back, and resets the board.

const KEYS_OFFSET = 0x1b000, KEYS_SIZE = 0x2000;
// What a failed step means here: only the keys partition is ever written.
const keysSaid = {
  write: m => `Writing the keys failed (${m}). Nothing else was touched; try again.`,
  verify: m => `The keys read back don't match what was written (${m}). Try again.`,
  reset: m => `The keys are written, but the board didn't restart (${m}): unplug it and plug it in again.`,
};

// p is the panel (kept open while it writes); stopRuns stops an install, whose console may
// hold the port; lock(on) holds the install's controls meanwhile.
function keysOnly(o, keysReady, p, stopRuns, lock) {
  const status = h("p", { class: "small", id: "usb-keys-status", role: "status", "aria-live": "polite" });
  const bar = h("progress", { id: "usb-keys-progress", max: 100, value: 0, hidden: true });
  const go = h("button", { class: "btn primary", type: "button", id: "usb-keys-go", disabled: !keysReady || !("serial" in navigator) }, "Write the keys over USB");
  const say = (t, cls = "small") => { status.className = cls; status.textContent = t; };
  go.addEventListener("click", async () => {
    go.disabled = true;
    bar.hidden = true;
    lock(true);
    let step = "port", transport = null;
    try {
      // The port first, while the click still counts as one.
      const port = await navigator.serial.requestPort();
      stopRuns(); // an install's console on this port
      await held;
      step = "prepare";
      say("Preparing your keys…");
      const b = await send("api/build/keys", {});
      const r = await api(`build/${encodeURIComponent(str(b.id))}/image.bin`);
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      const keys = new Uint8Array(await r.arrayBuffer());
      if (keys.length !== KEYS_SIZE) throw new Error(`the keys partition is ${keys.length} bytes, not ${KEYS_SIZE}`);
      const k = obj(b.keys);
      step = "connect";
      say("Connecting to the board…");
      const c = await flash.connect(port);
      transport = c.transport;
      if (!p.aside.isConnected) return; // the panel closed meanwhile: nothing written
      const { loader, description } = c;
      step = "write";
      p.busy(BUSY.keys);
      say(`Connected to ${description}. Writing the keys (release ${str(k.release)}, recovery ${str(k.recovery)})…`);
      bar.hidden = false;
      bar.value = 0;
      await flash.write(loader, [{ data: keys, address: KEYS_OFFSET }],
        { progress: (written, total) => { bar.value = total ? Math.round(written / total * 100) : 100; } });
      step = "verify";
      say("Reading it back…");
      const back = await loader.readFlash(KEYS_OFFSET, KEYS_SIZE);
      if (back.length < KEYS_SIZE || keys.some((x, i) => back[i] !== x)) throw new Error("they differ");
      step = "reset";
      say("Restarting the board…");
      p.busy("");
      await flash.reset(transport, loader);
      bar.value = 100;
      say(`Done: the board has your keys (release ${str(k.release)}, recovery ${str(k.recovery)}). Its firmware and settings are as they were; it shows on Nodes again in a moment.`, "small ok");
    } catch (e) {
      say(flash.serialError(e, step, keysSaid), "small bad");
    } finally {
      p.busy("");
      await hold(flash.close(transport));
      lock(false);
      go.disabled = !keysReady;
    }
  });
  return h("details", { class: "sub-ex", id: "usb-keys-only", open: !!o.keys },
    h("summary", { class: "small" }, "Write keys only (keeps settings)"),
    h("div", { class: "stack" },
      h("p", { class: "small" }, o.keys
        ? "This board has no keys, so it takes nothing over the network. This writes your keys and nothing else; its firmware and settings stay."
        : "For a board that runs espDNS but has no keys (+ Add node says so): writes your keys and nothing else."),
      h("div", { class: "row" }, go), bar, status));
}

// ---- the panel ----------------------------------------------------------------------------
// openUSB opens it. o: board (a board's name, chosen), image (a chip image: a board not in
// the list opens the custom form on it), address and gateway (prefilled), update (a found
// node's firmware updated: the title says so), reinstall (one of your nodes installed again,
// from its page: the title says so), at (the address it has: on DHCP, waited for there),
// name (what the board is called in the title, and the new node's name), found(node) (the
// node installed, once it answers on the network: Add it now closes the panel and calls
// it), waiting() (a new board handed to the controller with OK: the panel is closed; without
// it, a new board is waited for here as with + Add node).

// The install's steps; how long things are given; what the panel says while closing it
// would break the board; what each way of being on the network means once installed.
const STEP = { connect: "Connect", chip: "Check the chip", erase: "Erase the flash", write: "Write espDNS", restart: "Restart",
  wifi: "Wi-Fi", net: "On your network" };
const WAIT_POLL = 3000, HINT_AFTER = 60000, NOLOG_AFTER = 8000, STATE_TRIES = 15, JOIN_FOR = 45000, KEY_WAIT = 5000;
const OWN_USB = 0x303a; // Espressif's USB vendor ID: the chip's own USB, not a USB-to-serial chip
const BUSY = { erase: "Erasing the board: don't close this or unplug it until it's done.",
  write: "Writing to the board: don't close this or unplug it until it's done.",
  keys: "Writing the keys: don't close this or unplug it until it's done." };
const SAID = { erase: m => `Erasing failed (${m}). Unplug the board, plug it back in, and try again.` };
const INSTALLED = {
  wired: "Installed. Plug its network cable in: it's connected to this computer over USB only. It can stay on USB for power, or be unplugged and powered where its cable is.",
  joined: "Installed, and on your Wi-Fi network. USB can stay in for power.",
  ethernet: "Installed. It didn't ask for Wi-Fi, so it uses its Ethernet add-on. Plug its network cable in: it's connected to this computer over USB only.",
  elsewhere: "Installed. Set up its Wi-Fi over the board's own USB port (Install again there), or plug in its Ethernet add-on.",
};
// A new board's: what each way of being on the network means, once OK hands it to the
// controller (then: what happens next, by its address or on DHCP).
const ADDS = {
  wired: then => `Installed. Plug it into your network: ${then}`,
  joined: then => `Installed, and on your Wi-Fi network: ${then}`,
  elsewhere: then => `${INSTALLED.elsewhere} Then ${then}`,
};
ADDS.ethernet = ADDS.wired;
const THEN = { fixed: "it's added to your nodes as soon as it answers.", dhcp: "Nodes shows it once it answers, to reserve its address and add it.",
  back: "it gets its settings back as soon as it answers." };
const NAME = /^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$/; // as + Add node's (addnode.js)
const macOf = s => str(s).toLowerCase().replace(/[^0-9a-f]/g, "");
const colons = m => m.replace(/(..)(?!$)/g, "$1:");
const spinning = text => h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), text);

export async function openUSB(o = {}) {
  await loadCSS();
  // What runs for an install (the board's port, waiting for the node): stopped when the
  // panel closes or another image is prepared.
  let stops = [];
  const stopAll = () => { stops.forEach(f => f()); stops = []; };
  const p = panel(o.keys ? `Write keys to ${o.name || "it"} over USB` : o.update ? `Update ${o.name || "it"} over USB`
    : o.reinstall ? `Reinstall ${o.name || "it"} over USB` : "Install a board over USB", stopAll);
  p.aside.classList.add("usb");
  const supported = "serial" in navigator;
  p.body.append(h("p", { class: "small" }, o.keys
    ? "Plug the board into this computer with a USB cable. Writing the keys keeps everything else; a fresh install (below) erases the board."
    : "Plug the board into this computer with a USB cable. Installing erases it, then it starts espDNS on the address below."),
  ...(supported ? [] : [h("p", { class: "hint bad", id: "usb-unsupported" }, window.isSecureContext
    ? "This browser can't reach USB devices: use Chrome or Edge on this computer."
    : "USB needs this page over https, or on this computer (localhost).")]));
  const loading = h("p", { class: "hint" }, "Reading the boards…");
  p.body.append(loading);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Close"));

  let entries, known, key, labels = {};
  // Which firmware each chip image is (project release or your build), for the board's facts
  const readLabels = async () => {
    try { labels = Object.fromEntries(arr(obj(await readFirmware()).images).map(i => [str(i.image), labelOf(i)])); } catch { labels = {}; }
  };
  try {
    [entries, known, key] = await Promise.all([read("api/boards"), read("api/images").then(r => obj(obj(r).known)), read("api/key").then(obj), readLabels()]);
  } catch (e) {
    loading.className = "hint bad";
    loading.textContent = `Can't read the boards: ${e.message}`;
    return p;
  }
  entries = arr(entries);
  loading.remove();
  // A node is installed with your keys (the release key's and the recovery key's public
  // halves, in its keys partition): none, and nothing is prepared.
  const keysReady = !!key.present && !!obj(key.recovery).present;
  if (!keysReady) {
    p.body.append(h("p", { class: "hint bad", id: "usb-nokeys" }, "Set up your keys first: the board is installed with them. ",
      h("a", { href: "setup.html#next" }, "Make your keys")));
  }
  const writeKeys = keysOnly(o, keysReady, p, () => stops.forEach(f => f()), on => lock(on));
  if (o.keys) p.body.append(writeKeys);

  // The board: a list to pick from, the board's facts, or the custom form.
  // The library is long, so a search box filters it as you type (maker, model, catalog
  // name, chip image, w5500, poe…), and the list is grouped by maker (the title's first
  // word), your own boards first, each group in title order. Each option says what the
  // board has: flash, PSRAM, wired or Wi-Fi, and its tier unless tested.
  const q = h("input", { class: "t", id: "usb-q", type: "search", placeholder: "Search: maker, model, chip, w5500, poe", "aria-label": "Search boards", autocomplete: "off", spellcheck: "false" });
  const pick = h("select", { class: "t", id: "usb-board" });
  const titleOf = e => str(e.board.title) || e.board.name;
  const makerOf = e => e.source === "custom" ? "Your boards" : titleOf(e).split(/\s+/)[0];
  const netWord = b => { const n = obj(b.ethernet); return !n.kind || n.kind === "none" ? "Wi-Fi" : n.optional ? "Wi-Fi or wired" : "wired"; };
  const option = (e, chosen) => {
    const b = e.board, mem = [b.flash_mb && `${b.flash_mb} MB flash`, b.psram_mb && `${b.psram_mb} MB PSRAM`].filter(Boolean).join(", ");
    return h("option", { value: b.name, selected: chosen === b.name },
      `${titleOf(e)}${e.source === "custom" ? " (yours)" : ""}${mem ? ` · ${mem}` : ""} · ${netWord(b)}${b.tier && b.tier !== "tested" ? ` · ${b.tier}` : ""}`);
  };
  const haystack = e => {
    const b = e.board, n = obj(b.ethernet);
    return `${titleOf(e)} ${b.name} ${makerOf(e)} ${str(b.image)} ${str(n.kind)} ${str(n.phy)} ${netWord(b)} ${e.source === "custom" ? "yours mine" : ""}`.toLowerCase();
  };
  const fill = chosen => {
    const words = q.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const groups = new Map();
    let matched = 0;
    for (const e of entries) {
      // What matches every word typed; the board chosen stays, so the list keeps its value.
      const hay = haystack(e);
      const hit = words.every(w => hay.includes(w));
      if (hit) matched++;
      if (!hit && e.board.name !== chosen) continue;
      const m = makerOf(e);
      if (!groups.has(m)) groups.set(m, []);
      groups.get(m).push(e);
    }
    const makers = [...groups.keys()].sort((a, b) => a === "Your boards" ? -1 : b === "Your boards" ? 1 : a.localeCompare(b));
    pick.replaceChildren(h("option", { value: "" }, words.length && !matched ? "No board matches" : "Choose…"),
      ...makers.map(m => h("optgroup", { label: m },
        ...groups.get(m).sort((a, b) => titleOf(a).localeCompare(titleOf(b))).map(e => option(e, chosen)))),
      h("option", { value: "+", selected: chosen === "+" }, "Describe your own board…"));
  };
  q.addEventListener("input", () => fill(pick.value));
  const facts = h("div", { class: "stack", id: "usb-facts" });

  // The node's address, which goes into its board partition (it never asks DHCP on its own).
  const addrIn = h("input", { class: "t mono", id: "usb-addr", type: "text", value: str(o.address), placeholder: "192.0.2.52/24, or dhcp", spellcheck: "false", autocomplete: "off" });
  const gwIn = h("input", { class: "t mono", id: "usb-gw", type: "text", value: str(o.gateway), placeholder: "192.0.2.1", spellcheck: "false", autocomplete: "off" });
  const txIn = h("input", { class: "t", id: "usb-tx", type: "number", min: 2, max: 20, placeholder: "board default" });
  // A new board (from Nodes or the first run, not + Add node, nor one of yours again) is
  // added by its name.
  const adds = !!o.waiting && !o.update && !o.reinstall;
  const nameIn = h("input", { class: "t mono", id: "usb-name", type: "text", value: str(o.name), placeholder: "dns3", maxlength: 32, spellcheck: "false", autocomplete: "off" });
  const network = () => {
    const a = addrIn.value.trim();
    if (!a) throw new Error("Give the node an address, like 192.0.2.52/24.");
    return a === "dhcp" ? { address: a } : { address: a, gateway: gwIn.value.trim() };
  };
  const addrBox = h("div", { class: "stack" },
    ...(adds ? [h("label", { class: "f" }, h("span", {}, "Name"), nameIn, h("span", { class: "hint" }, "Lowercase letters, digits and dashes, like dns3."))] : []),
    h("div", { class: "grid2" },
      h("label", { class: "f" }, h("span", {}, "Address"), addrIn, h("span", { class: "hint" }, "With its prefix length, like 192.0.2.52/24; dhcp only where a DHCP server answers.")),
      h("label", { class: "f" }, h("span", {}, "Gateway"), gwIn)),
    h("label", { class: "f", id: "usb-tx-f" }, h("span", {}, "Wi-Fi transmit cap, dBm (optional)"), txIn));

  const probs = h("div", { class: "stack nogap", id: "usb-problems" });
  const out = h("div", { class: "stack", id: "usb-install", "aria-live": "polite" });
  const prep = h("button", { class: "btn primary", type: "button", id: "usb-prepare", disabled: true }, "Prepare image");
  const save = h("button", { class: "btn", type: "button", id: "usb-save", hidden: true }, "Save to my boards");
  let form = null, cur = null;

  function showBoard(e) {
    cur = e;
    form = null;
    const b = e.board;
    const rows = [
      ["Chip image", str(b.image)],
      ...(e.image_ready ? [["Firmware", labels[str(b.image)] || "your build"]] : []),
      ["Flash and PSRAM", `${b.flash_mb || 4} MB, ${b.psram_mb ? `${b.psram_mb} MB PSRAM` : "no PSRAM"}`],
      ["Network", summary(b)],
      ["LED", !b.led || b.led.kind === "none" ? "none" : `${str(b.led.kind)} on GPIO ${b.led.pin}`],
    ];
    facts.replaceChildren(
      h("dl", { class: "kv" }, rows.flatMap(([k, v]) => [h("dt", {}, k), h("dd", {}, v)])),
      // (replaceChildren would write a null as the text "null": the box or nothing.)
      ...(b.image && !e.image_ready ? [h("div", { class: "stack", id: "usb-noimage" },
        h("p", { class: "hint bad" }, `Chip image ${str(b.image)} isn't in the controller yet.`),
        firmwareBox({ id: "usb-fw", images: [str(b.image)], done: async () => {
          await readLabels();
          entries = arr(await read("api/boards"));
          fill(b.name);
          choose(b.name);
        } }))] : []),
      h("div", { class: "row" }, h("button", { class: "btn small link", type: "button", id: "usb-copy",
        onclick: () => showForm(copyOf(b)) }, "Describe a board from this one")));
    problems(probs, e.errors, e.warnings);
    txIn.placeholder = b.wifi && b.wifi.tx_power_dbm ? `${b.wifi.tx_power_dbm} (board default)` : "20 (chip default)";
    prep.disabled = !keysReady || arr(e.errors).length > 0 || !e.image_ready;
    save.hidden = true;
    addrBox.querySelector("#usb-tx-f").hidden = false;
    out.replaceChildren();
  }

  function showForm(from) {
    cur = null;
    pick.value = "+";
    const images = Object.keys(known).sort().map(k => [k]);
    form = h("form", { class: "stack usb-form", autocomplete: "off", id: "usb-form" },
      sections(images).map(([legend, fields]) => legend
        ? h("fieldset", {}, h("legend", {}, legend), h("div", { class: "grid3" }, fields.map(field)))
        : h("div", { class: "grid3" }, fields.map(field))));
    form.addEventListener("submit", e => e.preventDefault());
    if (images.length) form.elements.image.value = images.some(([k]) => k === "esp32s3-octal") ? "esp32s3-octal" : images[0][0];
    if (from) boardToForm(form, from);
    const showFor = () => {
      for (const l of form.querySelectorAll("[data-for]")) {
        const fs = l.closest("fieldset");
        l.hidden = !!fs && !l.dataset.for.split(" ").includes(fs.querySelector("select").value);
      }
    };
    let timer;
    const check = () => {
      showFor();
      clearTimeout(timer);
      timer = setTimeout(async () => {
        try { const r = await send("api/boards/check", formToBoard(form)); problems(probs, r.errors, r.warnings); prep.disabled = !keysReady || arr(r.errors).length > 0; }
        catch (err) { problems(probs, [err.message], []); }
      }, 250);
    };
    form.addEventListener("input", check);
    form.addEventListener("change", check);
    facts.replaceChildren(h("p", { class: "hint" }, "Any ESP32-S3 or ESP32-P4 board with 8 MB or more of PSRAM, from its pinout. Save it and it's in the list next time."), form);
    addrBox.querySelector("#usb-tx-f").hidden = true; // the form has its own
    save.hidden = false;
    prep.disabled = !keysReady;
    out.replaceChildren();
    check();
  }

  // copyOf is a board to describe from: b's pins, a name of its own to give.
  const copyOf = b => ({ ...structuredClone(b), name: "", title: `${str(b.title) || b.name} (mine)`, tier: "untested" });
  function choose(name) {
    if (name === "+") {
      // From the board chosen; else, for a board the list doesn't have, its name and chip image.
      showForm(cur ? copyOf(cur.board) : o.image && Object.hasOwn(known, o.image) ? { name: str(o.board), image: o.image } : null);
      return;
    }
    const e = entries.find(x => x.board.name === name);
    if (e) { showBoard(e); return; }
    cur = null; form = null;
    facts.replaceChildren(h("p", { class: "hint" }, "Choose the board plugged in, or describe your own."));
    probs.replaceChildren();
    prep.disabled = true;
    save.hidden = true;
    out.replaceChildren();
  }
  pick.addEventListener("change", () => choose(pick.value));

  save.addEventListener("click", async () => {
    if (!form) return;
    save.disabled = true;
    try {
      const b = await send("api/boards", formToBoard(form));
      entries = arr(await read("api/boards"));
      fill(b.name);
      choose(b.name);
    } catch (e) { problems(probs, [e.message], []); }
    save.disabled = false;
  });

  prep.addEventListener("click", async () => {
    let body;
    try {
      if (adds && !NAME.test(nameIn.value.trim())) throw new Error("Give the node a name: lowercase letters, digits and dashes, like dns3.");
      const net = network();
      if (form) body = { board: formToBoard(form), network: net };
      else if (cur) {
        body = { name: cur.board.name, network: net };
        if (txIn.value) {
          const changed = structuredClone(cur.board);
          changed.wifi = { ...(cur.board.wifi || {}), tx_power_dbm: parseInt(txIn.value, 10) };
          body = { board: changed, network: net };
        }
      } else return;
    } catch (e) { out.replaceChildren(h("p", { class: "hint bad" }, e.message)); return; }
    prep.disabled = true;
    stopAll();
    out.replaceChildren(spinning("Preparing the image…"));
    try {
      const build = await send("api/build", body);
      const c = obj(build.chip), k = obj(build.keys), b = obj(build.board);
      const go = h("button", { class: "btn primary", type: "button", id: "usb-go", disabled: !supported }, "Install on the board over USB");
      const erase = h("input", { type: "checkbox", id: "usb-erase" });
      const box = h("div", { class: "stack" });
      out.replaceChildren(h("p", { class: "small" }, `Ready: ${str(b.title) || str(b.name)}, ${str(c.image)} ${str(c.version)}, with your keys (release `,
        h("span", { class: "mono" }, str(k.release)), ", recovery ", h("span", { class: "mono" }, str(k.recovery)), ")."),
        h("label", { class: "check" }, erase, h("span", {}, "Erase the whole flash first (slower)")),
        h("div", { class: "row" }, go),
        ...(build.wifi ? [h("p", { class: "hint", id: "usb-wifi" },
          "After it installs, this panel asks for your Wi-Fi network and password; they go to the board over USB, not through the controller.")] : []),
        h("p", { class: "hint mono" }, `built ${str(c.built)} · ${str(c.elf_sha256).slice(0, 12)} · image ${str(build.id)}`),
        box);
      installer(box, go, erase, build, body.network);
    } catch (e) {
      out.replaceChildren(h("p", { class: "hint bad" }, e.message));
    }
    prep.disabled = !keysReady;
  });

  // lock(true) holds the panel's other controls while an install runs (a new image prepared,
  // or another board chosen, would drop it); lock(false) gives them back as they were.
  let locked = null;
  const lock = on => {
    if (on && !locked) {
      locked = [pick, q, prep, save, ...p.body.querySelectorAll("#usb-go, #usb-erase, #usb-keys-go")].map(e => [e, e.disabled]);
      for (const [e] of locked) e.disabled = true;
    } else if (!on && locked) {
      for (const [e, d] of locked) e.disabled = d;
      locked = null;
    }
  };

  // installer runs the install from go, in box: the steps (connect, check the chip, erase,
  // write, restart, Wi-Fi, on your network), what went wrong, the hints, the Board log.
  // Then it waits for the node on the network: at its address (POST /api/nodes/lookup; the
  // one Improv said, on Wi-Fi), or, on DHCP, as a node of this board not found before
  // (/api/nodes/found). One that answers is said, with Add it now when there is o.found
  // (+ Add node, with it chosen). Nothing after a minute: what to check, still waiting.
  function installer(box, go, erase, build, net) {
    const board = obj(build.board), wifi = !!build.wifi;
    // A node of yours reinstalled on DHCP isn't found again (it's listed): it is waited for
    // at the address it had (o.at).
    let ip = net.address === "dhcp" ? str(o.at) : str(net.address).split("/")[0];
    const steps = h("ol", { class: "steps", id: "usb-steps", hidden: true });
    const msg = h("div", { class: "stack", id: "usb-after" }, h("p", { class: "hint" }, "Once it's installed, this panel waits for it to show up on your network."));
    const hints = h("div", { class: "stack nogap", id: "usb-hints" });
    let con = null, transport = null, gen = 0, halt = null, timers = [], onFrame = null;
    // began: when the install began (performance.now(); a node answering with a shorter
    // uptime has restarted since). mac: the chip's, as the install read it (the node ID it
    // will report). noLink: its console's last health line, past booting, says no link.
    // nodeKey: the node key its console said (the public half), for a new board's OK.
    // chip: the chip on the port, as the install read it. booted: when the panel last
    // restarted it (its uptime at OK, for one of your nodes installed again).
    let began = 0, mac = "", noLink = false, nodeKey = "", chip = "", booted = 0;
    const log = logView(() => `espdns-${str(board.name) || "board"}-log.txt`, () => con && con.restart());
    box.replaceChildren(steps, msg, hints, log.box);
    const say = (...kids) => msg.replaceChildren(...kids.filter(Boolean));
    const hint = (id, text) => hints.append(h("p", { class: "hint", id }, text));
    const stopRun = () => {
      gen++;
      timers.forEach(clearTimeout);
      timers = [];
      if (halt) { halt(); halt = null; }
      p.busy("");
      log.restartable(false);
      hold(Promise.all([flash.close(transport), con && con.close()]));
      transport = con = onFrame = null;
    };
    stops.push(stopRun);
    // On DHCP, the nodes found before it: a new one of this board is it (or, for a node of
    // yours waited for at its old address, the same node found moved to another).
    const keyOf = n => `${str(n.id)}|${str(n.host)}|${str(n.version)}|${str(n.install)}`;
    const readFound = async () => arr(obj(await getJSON("api/nodes/found")).nodes);
    let before = new Set();
    if (net.address === "dhcp") readFound().then(ns => { before = new Set(ns.map(keyOf)); }).catch(() => { /* none known */ });

    // The steps, each a row of ol.steps: step(name, state, hint, ...more) sets one.
    let rows = {};
    const draw = names => {
      rows = {};
      steps.replaceChildren(...names.map(n => {
        const r = { hint: h("div", { class: "hint" }), more: h("div", { class: "stack" }) };
        r.li = h("li", { id: `usb-step-${n}` }, h("span", { class: "m" }), h("div", {}, h("b", {}, STEP[n]), r.hint, r.more));
        rows[n] = r;
        return r.li;
      }));
      steps.hidden = false;
      steps.scrollIntoView({ block: "nearest" });
    };
    const step = (n, state, text, ...more) => {
      const r = rows[n];
      if (!r) return;
      r.li.className = state;
      if (text !== undefined) r.hint.textContent = text;
      if (more.length) r.more.replaceChildren(...more);
    };
    const failed = (n, what, next) => {
      p.busy("");
      step(n, "failed");
      say(h("p", { class: "small bad", id: "usb-failed" }, what), next && h("p", { class: "hint" }, next));
      log.box.open = true;
      lock(false);
    };

    go.addEventListener("click", async () => {
      stopRun(); // the port, as the last install left it
      const my = gen, live = () => my === gen && box.isConnected, wipe = erase.checked;
      began = performance.now();
      mac = "";
      noLink = false;
      nodeKey = "";
      lock(true);
      say();
      hints.replaceChildren();
      steps.hidden = true;
      log.clear();
      log.box.open = false;
      let port, image, others;
      // The port first, while the click still counts as one; the ports there besides (the
      // console looks for the board's own USB again after the restart, never on another board's).
      try {
        port = await navigator.serial.requestPort();
        others = (await navigator.serial.getPorts()).filter(x => x !== port);
        await held;
      } catch (e) {
        lock(false);
        say(h("p", { class: "small bad", id: "usb-failed" }, flash.serialError(e, "port")));
        return;
      }
      try {
        const r = await api(`build/${encodeURIComponent(str(build.id))}/image.bin`);
        if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
        image = new Uint8Array(await r.arrayBuffer());
      } catch (e) {
        lock(false);
        say(h("p", { class: "small bad", id: "usb-failed" }, `Couldn't read the image (${e.message}): press Prepare image again.`));
        return;
      }
      if (!live()) return;
      const own = port.getInfo().usbVendorId === OWN_USB, kind = own ? "the board's own USB" : "a USB-to-serial chip";
      draw(["connect", "chip", ...(wipe ? ["erase"] : []), "write", "restart", ...(wifi ? ["wifi"] : []), "net"]);
      let n = "connect";
      try {
        step("connect", "now", `Over ${kind}…`);
        // Stopped while it connects (the panel closed): the port is let go of, nothing written.
        const c = await hold(flash.connect(port, l => { for (const x of clean(l).split("\n")) if (x.trim() && !/^Writing at 0x/.test(x)) log.add(x); })
          .then(async x => { if (!live()) { await flash.close(x.transport); throw new Error("stopped"); } return x; }));
        transport = c.transport;
        step("connect", "done", `Over ${kind}: ${c.description}`);
        mac = /^0*$/.test(macOf(c.mac)) ? "" : macOf(c.mac);
        chip = str(c.chip);
        n = "chip";
        step("chip", "now", "");
        // The image's chip (image.json's chip_family, else its chip: esp32s3 is ESP32-S3), and the
        // chip revisions its bootloader runs on (the two ESP32-P4 images: v1.x, v3.x).
        const ch = obj(build.chip), want = str(ch.chip_family) || str(ch.chip).toUpperCase().replace(/^ESP32(?=.)/, "ESP32-");
        const revs = flash.revisions(image, c.loader.chip.BOOTLOADER_FLASH_OFFSET), v = r => `v${Math.floor(r / 100)}.${r % 100}`;
        const wrong = c.chip.toUpperCase() !== want.toUpperCase() ? `Board Mismatch ${c.chip} in usb, firmware for ${want || "another chip"}`
          : revs && c.rev !== null && (c.rev < revs.min || c.rev > revs.max)
            ? `Board Mismatch ${c.chip} ${v(c.rev)} in usb, firmware for ${want} ${v(revs.min)}${revs.max === 0xffff ? " and later" : ` to ${v(revs.max)}`}`
            : c.flashMB && board.flash_mb && c.flashMB < board.flash_mb ? `This board has ${c.flashMB} MB of flash; the image is for ${board.flash_mb} MB.` : "";
        if (wrong) {
          await flash.close(transport);
          transport = null;
          failed("chip", wrong, "Nothing was written. Choose the board that's plugged in above, then press Prepare image again.");
          return;
        }
        step("chip", "done", `${c.chip}${c.flashMB ? `, ${c.flashMB} MB flash` : ""}`);
        const bar = h("progress", { id: "usb-progress", max: 100, value: 0 });
        let writing = false;
        const writeNow = () => {
          writing = true;
          if (wipe) step("erase", "done", "Erased");
          n = "write";
          p.busy(BUSY.write);
          step("write", "now", "0%", bar);
        };
        if (wipe) { n = "erase"; p.busy(BUSY.erase); step("erase", "now", "The whole flash: a minute or two."); } else writeNow();
        await flash.write(c.loader, [{ data: image, address: 0 }], { erase: wipe, progress: (w, t) => {
          if (!writing) writeNow();
          bar.value = t ? Math.round(w / t * 100) : 100;
          step("write", "now", `${bar.value}%`);
        } });
        p.busy("");
        step("write", "done", "100%");
        n = "reset";
        step("restart", "now", "");
        await flash.reset(transport, c.loader);
        booted = performance.now();
        await flash.close(transport);
        transport = null;
        step("restart", "done", "Restarted into espDNS");
      } catch (e) {
        if (!live()) return;
        await flash.close(transport);
        transport = null;
        failed(n === "reset" ? "restart" : n, flash.serialError(e, n, SAID));
        return;
      }
      go.textContent = "Install again";
      go.classList.remove("primary");
      lock(false);
      await watchBoard(port, own, live, others);
    });

    // watchBoard reads the board's console into the Board log once it restarts, asks for Wi-Fi
    // on a board that may run on it, then waits for it on the network.
    async function watchBoard(port, own, live, others) {
      log.add("--- the board's console ---");
      if (!own) hint("usb-uart", "Logs and Wi-Fi setup use the board's own USB port; this one only shows the bootloader.");
      let app = false;
      // Stopped while it opens: openConsole lets the port go itself.
      const c = await hold(openConsole(port, { others, live, line: l => {
        log.add(l);
        if (/^[IWE] \(\d+\)/.test(l)) app = true;
        const k = /nodekey: node key ([A-Za-z0-9+/=]+)/.exec(l);
        if (k) nodeKey = k[1];
        if (/"reasons":/.test(l)) noLink = !/"state":"booting"/.test(l) && /"reasons":\[[^\]]*"no link"/.test(l);
      },
        frame: (t, d) => onFrame && onFrame(t, d) }).catch(e => { if (live()) log.add(`(couldn't open the port again: ${str(e && e.message)})`); return null; }));
      if (!live()) return;
      con = c;
      log.restartable(!!con);
      timers.push(setTimeout(() => {
        if (!app && live()) hint("usb-nolog", "If it shows nothing, press RESET or unplug it and plug it in again.");
      }, NOLOG_AFTER));
      let how = wifi ? "ethernet" : "wired";
      if (wifi && !(own && con)) {
        step("wifi", "", "Not over this port: plug it into the board's own USB port to set up Wi-Fi.");
        how = "elsewhere";
      } else if (wifi) {
        const got = await askWiFi(live);
        if (!live()) return;
        if (got) { ip = got; how = "joined"; }
      }
      // Its chip MAC read: one of your nodes installed again, or a new board, is handed to the
      // controller with OK.
      if (mac) {
        let mine = null;
        try { mine = arr(await getJSON("api/nodes")).find(n => n.source === "settings" && macOf(n.id) === mac) || null; } catch { /* not known */ }
        if (!live()) return;
        if (mine || adds) {
          // An ESP32-S3 logs its node key early in its boot, maybe before the console was open
          // again: not seen within a few seconds, the board is restarted once to say it again.
          if (/S3/i.test(chip) && con && !nodeKey) {
            await keyLine(live);
            if (!nodeKey && live() && con) {
              log.add("--- restarting the board to read its node key ---");
              try { await con.restart(); booted = performance.now(); } catch { /* the port went */ }
              await keyLine(live);
            }
          }
          if (live()) ready(how, mine);
          return;
        }
      }
      wait(how);
    }

    // keyLine resolves once the console said the node key, or KEY_WAIT on.
    function keyLine(live) {
      const t0 = performance.now();
      return new Promise(resolve => {
        const tick = () => {
          if (nodeKey || !live() || performance.now() - t0 >= KEY_WAIT) resolve();
          else timers.push(setTimeout(tick, 200));
        };
        tick();
      });
    }

    // ready says a new board, or mine (one of your nodes, installed again), is installed and
    // what's next; its OK hands it to the controller (POST /api/nodes/waiting), which adds it,
    // or sends it its settings back, once it answers. An error stays here.
    function ready(how, mine) {
      const dhcp = net.address === "dhcp";
      step("net", "next", how === "wired" || how === "ethernet" ? "Plug its network cable in." : mine ? "It gets its settings back once it answers."
        : dhcp ? "Shown on Nodes once it answers." : "It's added once it answers.");
      // Its address as installed in its image: where it comes up. One of yours listed at
      // another moves there once that one is silent.
      const at = { address: net.address, ...(dhcp ? {} : { gateway: str(net.gateway) }), ...(nodeKey ? { node_key: nodeKey } : {}) };
      const was = mine ? hostOnly(mine.addr) : "", now = dhcp ? "" : str(net.address).split("/")[0];
      const fresh = () => ({ name: nameIn.value.trim(), board: str(board.name), id: colons(mac), ...at });
      const back = () => ({ reinstall: true, id: colons(mac), board: str(board.name),
        name: str(o.name) || str(obj(obj(mine.status).config).name) || str(mine.hostname), ...at, uptime_s: Math.round((performance.now() - booted) / 1000) });
      const said = h("p", { class: "small bad", id: "usb-ok-error", role: "status", hidden: true });
      const ok = h("button", { class: "btn primary", type: "button", id: "usb-ok" }, "OK");
      ok.addEventListener("click", async () => {
        ok.disabled = true;
        said.hidden = true;
        try {
          if (!mine) await post("api/nodes/waiting", fresh());
          else {
            // Not one of your nodes after all (removed meanwhile): a new board, by the name typed.
            try { await post("api/nodes/waiting", back()); } catch (e) {
              if (!adds || !/not one of your nodes/.test(e.message)) throw e;
              await post("api/nodes/waiting", fresh());
            }
          }
        } catch (e) {
          said.textContent = e.message;
          said.hidden = false;
          ok.disabled = false;
          return;
        }
        p.close();
        if (o.waiting) o.waiting();
      });
      say(h("p", { class: "small", id: "usb-installed" }, ADDS[how](mine ? THEN.back : dhcp ? THEN.dhcp : THEN.fixed)),
        was && now && was !== now ? h("p", { class: "small", id: "usb-moves" }, `It was at ${was}; it comes up at ${now} and moves there once ${was} is silent.`) : null,
        h("div", { class: "actions" }, ok), said);
    }

    // askWiFi asks the board for its Wi-Fi network over Improv: its state every second, up to
    // 15 s; once it answers, its version, the networks it sees, and the form to join one.
    // It resolves to the address the board has once joined, or null when it never answered
    // (the Ethernet add-on fitted: no Improv).
    function askWiFi(live) {
      return new Promise(resolve => {
        const send = d => { if (con) con.send(IMPROV.RPC, d).catch(() => { /* gone */ }); };
        const nets = new Map();
        // A select, not a text box with a list: a browser's password manager takes a text box
        // next to a password box for a login and fills over it. "Another network…" types one.
        const other = h("option", { value: "" }, "Another network…");
        const pick = h("select", { class: "t", id: "usb-ssid-pick", name: "wifi-network" }, h("option", { value: "" }, "Looking for networks…"), other);
        const ssid = h("input", { class: "t", id: "usb-ssid", name: "wifi-other", autocomplete: "off", spellcheck: "false", maxlength: 32 });
        const typed = h("label", { class: "f", hidden: true }, h("span", {}, "Network name"), ssid);
        const pass = h("input", { class: "t", id: "usb-pass", name: "wifi-key", type: "password", autocomplete: "new-password", maxlength: 64,
          "data-lpignore": "true", "data-1p-ignore": true, "data-bwignore": true });
        const join = h("button", { class: "btn primary", type: "submit", id: "usb-join" }, "Join");
        const said = h("p", { class: "small", id: "usb-wifi-said", role: "status" });
        const form = h("form", { class: "stack", id: "usb-wifi-form", autocomplete: "off" },
          h("label", { class: "f" }, h("span", {}, "Network"), pick), typed,
          h("label", { class: "f" }, h("span", {}, "Password"), pass), h("div", { class: "row" }, join), said);
        const another = () => pick.selectedOptions[0] === other;
        pick.addEventListener("change", () => { typed.hidden = !another(); if (another()) ssid.focus(); });
        let answered = false, tries = 0, joining = "", info = "", until = null;
        const tell = (t, cls = "small") => { said.className = cls; said.textContent = t; };
        const joined = url => {
          let at = "";
          try { at = new URL(url).hostname; } catch { /* none */ }
          clearTimeout(until);
          onFrame = null;
          step("wifi", "done", `${info ? `${info} · ` : ""}joined ${joining || "your Wi-Fi"}`);
          rows.wifi.more.replaceChildren(...(at ? [h("p", { class: "small", id: "usb-wifi-at" }, `On your Wi-Fi at ${at}`)] : []));
          resolve(at || ip);
        };
        const notJoined = t => { clearTimeout(until); joining = ""; join.disabled = false; tell(t, "small bad"); };
        form.addEventListener("submit", e => {
          e.preventDefault();
          const s = another() ? ssid.value.trim() : pick.value, bytes = t => new TextEncoder().encode(t).length;
          if (!s) return;
          // As the firmware takes them (firmware/main/improv.c): a name up to 32 bytes, a password up to 64.
          if (bytes(s) > 32 || bytes(pass.value) > 64) {
            tell(bytes(s) > 32 ? "That network name is longer than Wi-Fi allows (32 bytes)." : "That password is longer than Wi-Fi allows (64 bytes).", "small bad");
            return;
          }
          joining = s;
          join.disabled = true;
          tell(`Joining ${s}…`);
          send(rpc(IMPROV.WIFI, joinData(s, pass.value)));
          clearTimeout(until);
          until = setTimeout(() => notJoined("Couldn't join in time: check the network and the password, and try again."), JOIN_FOR);
          timers.push(until);
        });
        onFrame = (type, d) => {
          if (!live()) return;
          if (type === IMPROV.STATE) {
            if (!answered) {
              answered = true;
              if (d[0] !== IMPROV.PROVISIONED) {
                step("wifi", "next", "Choose its network.", form);
                send(rpc(IMPROV.INFO));
                send(rpc(IMPROV.SCAN));
                tell("Looking for networks…");
                pick.focus();
              }
            }
          } else if (type === IMPROV.ERROR && joining && d[0]) {
            notJoined(d[0] === IMPROV.NO_CONNECT ? "Couldn't join: check the password" : `Couldn't join (error ${d[0]}).`);
          } else if (type === IMPROV.RESULT) {
            const r = result(d);
            if (r.cmd === IMPROV.INFO) {
              info = `${r.strs[0] || "espDNS"} ${r.strs[1] || ""} on ${r.strs[2] || ""}`.replace(/\s+/g, " ").trim();
              rows.wifi.hint.textContent = `${info} · choose its network.`;
            } else if (r.cmd === IMPROV.SCAN) {
              if (r.strs.length) {
                const [name, rssi, secure] = r.strs;
                if (name && !(nets.get(name)?.rssi >= Number(rssi))) nets.set(name, { rssi: Number(rssi), secure: secure === "YES" });
              } else {
                // The scan is over: strongest first, then "Another network…".
                const was = another();
                pick.replaceChildren(...[...nets].sort((a, b) => b[1].rssi - a[1].rssi)
                  .map(([nm, n]) => h("option", { value: nm }, `${nm} · ${n.rssi} dBm${n.secure ? " · \u{1F512}" : " · open"}`)), other);
                if (was || !nets.size) other.selected = true;
                typed.hidden = !another();
                if (!joining) tell(nets.size ? `${nets.size} network${nets.size === 1 ? "" : "s"} found: pick one.` : "No networks found: type its name.");
              }
            } else if ((r.cmd === IMPROV.WIFI || r.cmd === IMPROV.GET_STATE) && r.strs[0]) joined(r.strs[0]);
          }
        };
        step("wifi", "now", "Asking the board…");
        const ask = () => {
          if (answered || !live()) return;
          if (tries++ >= STATE_TRIES) {
            onFrame = null;
            step("wifi", "", "It didn't ask for Wi-Fi: it uses its Ethernet add-on.");
            resolve(null);
            return;
          }
          send(rpc(IMPROV.GET_STATE));
          timers.push(setTimeout(ask, 1000));
        };
        ask();
      });
    }

    function wait(how) {
      if (halt) halt();
      const my = gen, started = performance.now();
      const waitHint = h("div", { class: "stack nogap", id: "usb-wait-hint", hidden: true });
      const cable = how === "wired" || how === "ethernet";
      step("net", "now", cable ? "Plug its network cable in." : ip ? `Waiting for it at ${ip}…` : "Waiting for it on your network…");
      say(h("p", { class: "small", id: "usb-installed" }, INSTALLED[how]),
        h("div", { id: "usb-waiting" }, spinning(ip ? `Waiting for it at ${ip}…` : "Waiting for it on your network…")), waitHint);
      halt = every(async () => {
        if (!box.isConnected) { stopAll(); return; }
        let node = null, listed = false, other = false;
        try {
          if (ip) {
            const r = await post("api/nodes/lookup", { address: ip });
            // Only the board installed (its ID the chip's MAC), answering there now and
            // restarted since the install began, is it: one listed already is back only then.
            // Another node there has the address.
            const same = !mac || macOf(obj(r.node).id) === mac;
            if (r.found && r.node && same && typeof r.uptime_s === "number" && r.uptime_s * 1000 < performance.now() - began) {
              listed = !!r.listed; node = r.node;
            } else other = !!r.answered || (!!r.found && (mac ? !same : !r.listed));
          } else {
            node = (await readFound()).find(x => str(x.board) === str(board.name) && !before.has(keyOf(x)) && (!mac || macOf(x.id) === mac)) || null;
          }
          if (!node && ip && mac && net.address === "dhcp") node = (await readFound()).find(x => x.move && macOf(x.id) === mac && !before.has(keyOf(x))) || null;
        } catch { /* not there yet */ }
        if (my !== gen) return; // stopped meanwhile
        if (node) { halt(); halt = null; arrived(node, listed); return; }
        if (!other && !noLink && performance.now() - started < HINT_AFTER) { waitHint.hidden = true; waitHint.replaceChildren(); return; }
        waitHint.hidden = false;
        waitHint.replaceChildren(...nothingYet(other, how));
      }, WAIT_POLL);
    }

    function nothingYet(other, how) {
      const again = h("p", { class: "hint" }, "To use another address, change it above, press Prepare image and install again.");
      if (other) return [h("p", { class: "small bad" }, `Something else answers at ${ip}, so that address is taken.`), again];
      const check = how === "joined" ? "Check it's still on your Wi-Fi" : how === "elsewhere" ? "Check its Wi-Fi is set up, or its cable is in" : "Check the cable is in";
      if (noLink) return [h("p", { class: "small", id: "usb-nolink" }, `The board reports no network link. ${how === "wired" || how === "ethernet" ? "Plug its network cable in" : check}.`)];
      return [h("p", { class: "small" }, ip
        ? `Nothing yet. ${check}, and that ${ip} is free on your network and outside your router's DHCP pool.`
        : `Nothing yet. ${check}, and that your router hands out addresses on that network.`),
      ...(ip && net.gateway ? [h("p", { class: "hint" }, `Its gateway is ${net.gateway}. That has to be your router's address on this network.`)] : []),
      again];
    }

    function arrived(x, listed) {
      const at = hostOnly(x.host) || ip;
      step("net", "done", `At ${at}`);
      if (listed) { say(h("p", { class: "small", id: "usb-arrived" }, `It's back on your network at ${at}, and on Nodes as before.`)); return; }
      // A node of yours reinstalled at a new address: its ID is one settings.json lists (at
      // its old address, `x.move`). It's the same node moved, not a new one to add again.
      if (x.move && x.next === "settings-move") {
        const name = str(x.name) || str(x.hostname) || "Your node";
        const text = `It's ${name} at ${at} — the same node, moved from ${x.move}. Add it there to move its entry.`;
        if (o.found) {
          say(h("p", { class: "small", id: "usb-arrived" }, text),
            h("div", { class: "actions" }, h("button", { class: "btn primary", type: "button", id: "usb-add", onclick: () => { p.close(); o.found(x); } }, "Add it now")));
          return;
        }
        say(h("p", { class: "small", id: "usb-arrived" }, `${text} + Add node on Nodes has it: add it there.`));
        return;
      }
      if (o.found) {
        say(h("p", { class: "small", id: "usb-arrived" }, `It's on your network at ${at}. Next, add it to your nodes.`),
          h("div", { class: "actions" }, h("button", { class: "btn primary", type: "button", id: "usb-add", onclick: () => { p.close(); o.found(x); } }, "Add it now")));
        return;
      }
      say(h("p", { class: "small", id: "usb-arrived" }, `It's on your network at ${at}. Add it from + Add node on Nodes.`));
    }
  }

  p.body.append(
    h("label", { class: "f" }, h("span", {}, "Find the board"), q),
    h("label", { class: "f" }, h("span", {}, "The board"), pick), facts, probs,
    h("h3", { class: "small" }, "For this node"), addrBox,
    h("div", { class: "row" }, prep, save), out);
  // Write keys only: first for a board with no keys, else under Advanced.
  if (!o.keys) p.body.append(h("h3", { class: "small" }, "Advanced"), writeKeys);

  // The board asked for: in the list, or the custom form on its chip image.
  if (o.board && entries.some(e => e.board.name === o.board)) { fill(o.board); choose(o.board); }
  else if (o.image && Object.hasOwn(known, o.image)) { fill("+"); choose("+"); }
  else { fill(""); choose(""); }
  return p;
}

// usbFor is openUSB's options for a found node (/api/nodes/found): its board and chip image,
// the address it runs on (with its prefix, else /24) and its gateway; keys for one with no
// keys (setup mode: Write keys only first).
export function usbFor(f, name) {
  const a = str(f.address) || (hostOnly(f.host) ? `${hostOnly(f.host)}/24` : "");
  return { board: str(f.board), image: str(f.image), address: a, gateway: str(f.gateway), update: true, name, keys: f.install === "keys" };
}
