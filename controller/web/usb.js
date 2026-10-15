// Install over USB (its own button on Nodes and in the first run, a found node's "Update
// over USB", and a node page's "Reinstall over USB"): a panel that installs espDNS on a board plugged
// into this computer, with ESP Web Tools (the vendored copy, web/vendor/esp-web-tools). The
// board from the catalogue, or one of yours, or one described from its pinout (the custom
// board form, POST /api/boards/check, saved with POST /api/boards); the node's address,
// which goes into its board partition; Prepare image (POST /api/build) gives the install
// button for /build/<id>/manifest.json. Installing erases the board; on a board that may run
// on Wi-Fi, ESP Web Tools then asks for the network over Improv (image.Chip.Manifest). The
// image carries your keys (the release and recovery public keys, in its keys partition): until
// both are set up (the first run's Make your keys) nothing is prepared. Write keys only (POST
// /api/build/keys) writes the keys partition alone with the vendored esptool-js, not ESP Web
// Tools, for a node in setup mode, and keeps the rest. A board whose chip image isn't in the
// controller yet offers Get firmware (firmware.js: that chip image from the project's
// release, fetched, checked and imported); each board says which firmware it gets ("project release v0.0.3", "your
// build"). Once installed, the panel waits for the node on the network (POST
// /api/nodes/lookup at its address, or /api/nodes/found for one on DHCP) and, when it
// answers, says so with Add it now, which hands it to o.found (+ Add node, with it chosen and
// its address filled in). Installing and adding are two steps. The page that opens it needs
// websec.USBCSP: the flasher's "no port picked" dialog has two style attributes, and its
// Wi-Fi form three it sets first, allowed by hash there only.
import { h, arr, obj, str, panel, api, hostOnly, getJSON, post } from "./shell.js";
import { every } from "./common.js";
import { firmwareBox, readFirmware, labelOf } from "./firmware.js";

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

// The flasher: its module, once.
let flasher = null;
const loadFlasher = () => (flasher ||= import("./vendor/esp-web-tools/install-button.js"));

function summary(b) {
  const e = b.ethernet || { kind: "none" };
  const wired = e.kind === "emac" ? `${str(e.phy || "ip101").toUpperCase()} on the built-in MAC` : e.kind === "w5500" ? "W5500" : "";
  const net = !wired ? "Wi-Fi" : e.optional ? `Wi-Fi; wired instead if a ${wired} add-on is fitted` : `Wired (${wired})`;
  const sd = !b.sd || b.sd.kind === "none" ? "no SD" : b.sd.kind === "sdmmc" ? "SDMMC" : "SD over SPI";
  return `${net} · ${sd}`;
}

// onWiFi is whether the board may come up on Wi-Fi (boards.Board.MayRunOnWiFi): no Ethernet,
// or Ethernet that may not be fitted. Its manifest then has ESP Web Tools ask for the Wi-Fi
// network over Improv once it has installed (image.Chip.Manifest).
const onWiFi = b => !b.ethernet || b.ethernet.kind === "none" || !!b.ethernet.optional;

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

// ---- the panel ----------------------------------------------------------------------------
// openUSB opens it. o: board (a board's name, chosen), image (a chip image: a board not in
// the list opens the custom form on it), address and gateway (prefilled), update (a found
// node's firmware updated: the title says so), reinstall (one of your nodes installed again,
// from its page: the title says so), name (what the board is called in the title),
// found(node) (the node installed, once it answers on the network: Add it now closes the
// panel and calls it).
// flashButton is ESP Web Tools' install button for a prepared build's manifest, its activate
// slot our own button.
function flashButton(id, label, actId) {
  const btn = document.createElement("esp-web-install-button");
  btn.setAttribute("manifest", `build/${encodeURIComponent(str(id))}/manifest.json`);
  const act = h("button", { class: "btn primary", type: "button", slot: "activate", id: actId }, label);
  btn.append(act, h("span", { slot: "unsupported", class: "hint bad" }, "This browser can't reach USB devices: use Chrome or Edge."),
    h("span", { slot: "not-allowed", class: "hint bad" }, "USB needs this page over https, or on this computer (localhost)."));
  return btn;
}

// ---- after the install ------------------------------------------------------------------------
// ESP Web Tools fires no event as an install goes: its dialog (ewt-install-dialog, which the
// install button adds to the page) keeps it in _installState, {state, message}, the state
// "initializing", "manifest", "preparing", "erasing", "writing", then "finished" or "error".
// watchInstall reads that while a dialog is open and calls on(state, message) as it changes
// ("busy" for the ones before the end), and on("closed", the last state) when the dialog
// goes. It returns a function that stops it.
const INSTALL_POLL = 500, WAIT_POLL = 3000, HINT_AFTER = 60000;
function watchInstall(on) {
  let dialog = null, timer = null, last = "";
  const check = () => {
    const s = obj(dialog && dialog._installState), st = str(s.state);
    if (!st || st === last) return;
    last = st;
    on(st === "finished" || st === "error" ? st : "busy", str(s.message));
  };
  const drop = () => { clearInterval(timer); timer = null; dialog = null; };
  const mo = new MutationObserver(recs => {
    for (const r of recs) {
      for (const n of r.addedNodes) {
        if (n.localName !== "ewt-install-dialog") continue;
        drop();
        dialog = n; last = "";
        timer = setInterval(check, INSTALL_POLL);
      }
      for (const n of r.removedNodes) {
        if (!dialog || n !== dialog) continue;
        check();
        const ended = last;
        drop();
        on("closed", ended);
      }
    }
  });
  mo.observe(document.body, { childList: true });
  return () => { mo.disconnect(); drop(); };
}
const spinning = text => h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), text);

// ---- Write keys only ------------------------------------------------------------------------
// The controller's two public keys written to the board's keys partition and nothing else, for
// a node in setup mode (no keys, or corrupt ones): POST /api/build/keys makes the 8 KB
// partition image (/build/<id>/image.bin); the vendored esptool-js (web/vendor/esptool-js,
// the version ESP Web Tools' bundle has) connects to the board's bootloader, writes it at
// 0x1b000 with no erase of anything else (eraseAll false: esptool never sends an erase
// command; the flasher stub rewrites only the two sectors it is given), reads it back, and
// resets the board. No ESP Web Tools dialog: it has no way to offer erasing the flash here.

const KEYS_OFFSET = 0x1b000, KEYS_SIZE = 0x2000;
let esptool = null;
const loadEsptool = () => (esptool ||= import("./vendor/esptool-js/bundle.js"));

// keysWriteError is what went wrong, in words, for the step that failed.
function keysWriteError(e, step) {
  const m = String((e && e.message) || e);
  if (e && e.name === "NotFoundError") return "No port was chosen: nothing was written.";
  if (e && (e.name === "InvalidStateError" || e.name === "NetworkError") || /open/i.test(m) && /port/i.test(m)) {
    return "The board's port is busy: close the other tab or program using it (a serial monitor, another install) and try again.";
  }
  if (e && e.name === "SecurityError") return "The browser didn't allow the serial port: open this page on the controller's machine (localhost) in Chrome or Edge.";
  switch (step) {
    case "connect": return `Couldn't reach the board's bootloader (${m}). Unplug it, hold BOOT while you plug it back in, and try again.`;
    case "write": return `Writing the keys failed (${m}). Nothing else was touched; try again.`;
    case "verify": return `The keys read back don't match what was written (${m}). Try again.`;
    case "reset": return `The keys are written, but the board didn't restart (${m}): unplug it and plug it in again.`;
  }
  return m;
}

function keysOnly(o, keysReady) {
  const status = h("p", { class: "small", id: "usb-keys-status", role: "status", "aria-live": "polite" });
  const bar = h("progress", { id: "usb-keys-progress", max: 100, value: 0, hidden: true });
  const go = h("button", { class: "btn primary", type: "button", id: "usb-keys-go", disabled: !keysReady || !("serial" in navigator) }, "Write the keys over USB");
  const say = (t, cls = "small") => { status.className = cls; status.textContent = t; };
  go.addEventListener("click", async () => {
    go.disabled = true;
    bar.hidden = true;
    let step = "port", transport = null;
    try {
      // The port first, while the click still counts as one.
      const port = await navigator.serial.requestPort();
      step = "prepare";
      say("Preparing your keys…");
      const b = await send("api/build/keys", {});
      const r = await api(`build/${encodeURIComponent(str(b.id))}/image.bin`);
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      const keys = new Uint8Array(await r.arrayBuffer());
      if (keys.length !== KEYS_SIZE) throw new Error(`the keys partition is ${keys.length} bytes, not ${KEYS_SIZE}`);
      const k = obj(b.keys);
      const { ESPLoader, Transport } = await loadEsptool();
      step = "connect";
      say("Connecting to the board…");
      transport = new Transport(port, false);
      const quiet = { clean() {}, write() {}, writeLine() {} };
      const loader = new ESPLoader({ transport, baudrate: 115200, terminal: quiet });
      const chip = await loader.main();
      step = "write";
      say(`Connected to ${chip}. Writing the keys (release ${str(k.release)}, recovery ${str(k.recovery)})…`);
      bar.hidden = false;
      bar.value = 0;
      await loader.writeFlash({
        fileArray: [{ data: keys, address: KEYS_OFFSET }],
        flashMode: "keep", flashFreq: "keep", flashSize: "keep",
        eraseAll: false, compress: true,
        reportProgress: (_, written, total) => { bar.value = total ? Math.round(written / total * 100) : 100; },
      });
      step = "verify";
      say("Reading it back…");
      const back = await loader.readFlash(KEYS_OFFSET, KEYS_SIZE);
      if (back.length < KEYS_SIZE || keys.some((x, i) => back[i] !== x)) throw new Error("they differ");
      step = "reset";
      say("Restarting the board…");
      await loader.after("hard_reset");
      bar.value = 100;
      say(`Done: the board has your keys (release ${str(k.release)}, recovery ${str(k.recovery)}). Its firmware and settings are as they were; it shows on Nodes again in a moment.`, "small ok");
    } catch (e) {
      say(keysWriteError(e, step), "small bad");
    } finally {
      if (transport) { try { await transport.disconnect(); } catch { /* closed already */ } }
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

export async function openUSB(o = {}) {
  await loadCSS();
  // What runs after an install (watching the installer, waiting for the node): stopped when
  // the panel closes or another image is prepared.
  let stops = [];
  const stopAll = () => { stops.forEach(f => f()); stops = []; };
  const p = panel(o.keys ? `Write keys to ${o.name || "it"} over USB` : o.update ? `Update ${o.name || "it"} over USB`
    : o.reinstall ? `Reinstall ${o.name || "it"} over USB` : "Install a board over USB", stopAll);
  p.aside.classList.add("usb");
  const supported = "serial" in navigator;
  p.body.append(h("p", { class: "small" }, o.keys
    ? "Plug the board into this computer with a USB cable. Writing the keys keeps everything else; a fresh install (below) erases the board."
    : "Plug the board into this computer with a USB cable. Installing erases it, then it starts espDNS on the address below."),
  ...(supported ? [] : [h("p", { class: "hint bad", id: "usb-unsupported" }, "This browser can't reach USB devices: use Chrome or Edge on this computer.")]));
  const loading = h("p", { class: "hint" }, "Reading the boards…");
  p.body.append(loading);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Close"));
  loadFlasher().catch(() => { /* shown when the button is drawn */ });

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
  const writeKeys = keysOnly(o, keysReady);
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
  const network = () => {
    const a = addrIn.value.trim();
    if (!a) throw new Error("Give the node an address, like 192.0.2.52/24.");
    return a === "dhcp" ? { address: a } : { address: a, gateway: gwIn.value.trim() };
  };
  const addrBox = h("div", { class: "stack" },
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
      try { await loadFlasher(); } catch (e) { throw new Error(`The installer didn't load: ${e.message}`); }
      const btn = flashButton(build.id, "Install on the board over USB", "usb-go");
      const c = obj(build.chip), k = obj(build.keys);
      const after = h("div", { class: "stack", id: "usb-after" }, h("p", { class: "hint" }, "Once it's installed, this panel waits for it to show up on your network."));
      out.replaceChildren(h("p", { class: "small" }, `Ready: ${str(obj(build.board).title) || str(obj(build.board).name)}, ${str(c.image)} ${str(c.version)}, with your keys (release `,
        h("span", { class: "mono" }, str(k.release)), ", recovery ", h("span", { class: "mono" }, str(k.recovery)), ")."), btn,
        ...(onWiFi(obj(build.board)) ? [h("p", { class: "hint", id: "usb-wifi" },
          "After it installs, it asks for your Wi-Fi network and password; they go to the board over USB, not through the controller.")] : []),
        h("p", { class: "hint mono" }, `built ${str(c.built)} · ${str(c.elf_sha256).slice(0, 12)} · image ${str(build.id)}`),
        after);
      afterInstall(after, btn.querySelector("#usb-go"), body.network, obj(build.board));
    } catch (e) {
      out.replaceChildren(h("p", { class: "hint bad" }, e.message));
    }
    prep.disabled = !keysReady;
  });

  // afterInstall follows the install in ESP Web Tools' dialog and says how it went, in box.
  // Once it's done, it waits for the node on the network: at its address (POST
  // /api/nodes/lookup), or, on DHCP, as a node of this board not found before
  // (/api/nodes/found). One that answers is said, with Add it now when there is o.found
  // (+ Add node, with it chosen). Nothing after a minute: what to check, still waiting.
  function afterInstall(box, act, net, board) {
    const ip = net.address === "dhcp" ? "" : str(net.address).split("/")[0];
    const wifi = onWiFi(board);
    const say = (...kids) => box.replaceChildren(...kids);
    let gen = 0, halt = null;
    const stopWait = () => { gen++; if (halt) { halt(); halt = null; } };
    stops.push(stopWait);
    // On DHCP, the nodes found before it: a new one of this board is it.
    const keyOf = n => `${str(n.id)}|${str(n.host)}|${str(n.version)}|${str(n.install)}`;
    const readFound = async () => arr(obj(await getJSON("api/nodes/found")).nodes);
    let before = new Set();
    if (!ip) readFound().then(ns => { before = new Set(ns.map(keyOf)); }).catch(() => { /* none known */ });
    const failed = (what, next) => {
      stopWait();
      say(h("p", { class: "small bad", id: "usb-failed" }, what), h("p", { class: "hint" }, next));
    };

    stops.push(watchInstall((state, msg) => {
      if (!box.isConnected) { stopAll(); return; }
      if (state === "busy") {
        stopWait();
        say(spinning("Installing. Keep the board plugged in until the installer says it's done."));
      } else if (state === "finished") {
        wait();
      } else if (state === "error") {
        failed(`The install stopped: ${msg || "the installer hit an error"}`, `Unplug the board, plug it back in, and press ${act.textContent} to try again.`);
      } else if (state === "closed" && msg && msg !== "finished" && msg !== "error") {
        // The dialog closed partway through (msg is the last state it had).
        failed("The installer closed before it finished, so the board isn't ready.", `Press ${act.textContent} to start again.`);
      }
    }));

    function wait() {
      stopWait();
      const my = gen, started = Date.now();
      act.textContent = "Install again";
      const hint = h("div", { class: "stack nogap", id: "usb-wait-hint", hidden: true });
      say(h("p", { class: "small", id: "usb-installed" }, wifi
        ? `Installed. It joins the Wi-Fi network you enter in the installer${obj(board.ethernet).optional ? ", or uses Ethernet if its add-on is fitted" : ""}. USB can stay in for power.`
        : "Installed. Plug it into your network with an Ethernet cable; USB can stay in for power."),
      h("div", { id: "usb-waiting" }, spinning(ip ? `Waiting for it at ${ip}…` : "Waiting for it on your network…")), hint);
      halt = every(async () => {
        if (!box.isConnected) { stopAll(); return; }
        let node = null, listed = false, other = false;
        try {
          if (ip) {
            const r = await post("api/nodes/lookup", { address: ip });
            if (r.listed) { listed = true; node = obj(r.node); }
            else if (r.found && r.node) node = r.node;
            else other = !!r.answered;
          } else {
            node = (await readFound()).find(n => str(n.board) === str(board.name) && !before.has(keyOf(n))) || null;
          }
        } catch { /* not there yet */ }
        if (my !== gen) return; // stopped meanwhile
        if (node) { stopWait(); arrived(node, listed); return; }
        if (!other && Date.now() - started < HINT_AFTER) return;
        hint.hidden = false;
        hint.replaceChildren(...nothingYet(other));
      }, WAIT_POLL);
    }

    function nothingYet(other) {
      const again = h("p", { class: "hint" }, "To use another address, change it above, press Prepare image and install again.");
      if (other) return [h("p", { class: "small bad" }, `Something else answers at ${ip}, so that address is taken.`), again];
      const check = wifi ? "Check the installer said it joined your Wi-Fi" : "Check the cable is in";
      return [h("p", { class: "small" }, ip
        ? `Nothing yet. ${check}, and that ${ip} is free on your network and outside your router's DHCP pool.`
        : `Nothing yet. ${check}, and that your router hands out addresses on that network.`),
      ...(ip && net.gateway ? [h("p", { class: "hint" }, `Its gateway is ${net.gateway}. That has to be your router's address on this network.`)] : []),
      again];
    }

    function arrived(n, listed) {
      const at = hostOnly(n.host) || ip;
      if (listed) { say(h("p", { class: "small", id: "usb-arrived" }, `It's back on your network at ${at}, and on Nodes as before.`)); return; }
      if (o.found) {
        say(h("p", { class: "small", id: "usb-arrived" }, `It's on your network at ${at}. Next, add it to your nodes.`),
          h("div", { class: "actions" }, h("button", { class: "btn primary", type: "button", id: "usb-add", onclick: () => { p.close(); o.found(n); } }, "Add it now")));
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
