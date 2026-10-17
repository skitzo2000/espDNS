// Boards on a stubbed Web Serial port, for page.addInitScript(fn, options): navigator.serial
// hands out the one port, with no hardware behind it. Each function runs in the page, so it
// keeps all it needs inside itself; each port's open() hands out fresh streams, as a real
// port does, so it can be opened again after a close.

// fakeEsp is an ESP chip on a stubbed port. In its serial bootloader (as it starts), as far
// as esptool-js speaks to it: SLIP frames; SYNC answered (the ROM's non-zero value);
// registers read as 0, but the chip-detect magic (options.magic: 0x9 the ESP32-S3, the
// default; 0x0ADDBAD0 the ESP32-P4) and options.regs ({address: value}); the flasher stub's upload taken and its "OHAI" sent;
// compressed flash writes (FLASH_DEFL_BEGIN/DATA/END) inflated into a sparse flash (refused
// with options.failWrite; options.hold holds the first block until window.__esp.release()
// is called); READ_FLASH answered from it. options.syncs is how many SYNCs it
// answers (by default all; 0 is a board not in its bootloader). A reset (RTS raised, then
// dropped) with DTR raised in it goes back to the bootloader; one without (a hard reset)
// starts the app: it prints options.bootLog (ESP-IDF's coloured lines, \r\n; its public node key
// among them, as firmware/main/nodekey_node.c logs it) to the port,
// then, on a Wi-Fi board (options.wifi), speaks Improv Wi-Fi serial as firmware/main/improv.c
// does: ready, its info, two networks scanned, a join that works (options.join false: one
// refused, ERROR 3). options.vid is the port's USB vendor ID (0x303a: the chip's own USB).
// Every command's op is kept in window.__esp.ops, each write's offset and size in .begins,
// the flash written in .flash (offset -> byte), each setSignals call in .signals with .after,
// the number of commands before it, and the Wi-Fi join's data (as the firmware's RPC reads
// it) in .join; options.app starts it in the app; options.mac is its chip MAC (an ESP32-S3's
// eFuse words); options.keyLate leaves the node key line out of its first boot's log;
// window.__esp.print(lines) prints more console lines in the app. As a real port, it refuses to open while
// open (InvalidStateError) and to close while a stream is locked.
function fakeEsp({ magic = 0x9, regs = {}, mac = "aa:bb:cc:00:00:52", syncs = Infinity, failWrite = false, hold = false, wifi = false, join = true, vid = 0x303a, app = false, keyLate = false,
  bootLog = ["ESP-ROM:esp32s3-20210327", "rst:0x15 (USB_UART_CHIP_RESET),boot:0x8 (SPI_FAST_FLASH_BOOT)",
    "\x1b[0;32mI (311) espdns: espDNS 0.0.5 starting\x1b[0m", "\x1b[0;33mW (342) net: no link yet\x1b[0m",
    "\x1b[0;32mI (345) nodekey: node key BGsX0fLhLEJH+Lzm5WOkQPJ3A32BLeszoPShOUXYmMKWT+NC4v4af5uO5+tKfA+eFivOM1drMV7Oy7ZAaDe/UfU=\x1b[0m", "\x1b[0;32mI (350) improv: listening on USB serial\x1b[0m"] } = {}) {
  const esp = { ops: [], begins: [], flash: {}, opened: 0, signals: [], join: null, mode: app ? "app" : "rom" };
  const m = mac.split(":").map(x => parseInt(x, 16));
  regs = { [0x60007044]: (m[2] << 24 | m[3] << 16 | m[4] << 8 | m[5]) >>> 0, [0x60007048]: m[0] << 8 | m[1], ...regs };
  window.__esp = esp;
  let ctrl = null, synced = 0, rts = false, dtr = false, booted = !app, openedIn = "rom";
  const out = bytes => { try { ctrl.enqueue(bytes); } catch { /* closed */ } };
  const slip = data => {
    const o = [0xc0];
    for (const b of data) {
      if (b === 0xc0) o.push(0xdb, 0xdc); else if (b === 0xdb) o.push(0xdb, 0xdd); else o.push(b);
    }
    o.push(0xc0);
    return new Uint8Array(o);
  };
  const send = data => out(slip(data));
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
      case 0x08: if (synced++ < syncs) for (let i = 0; i < 8; i++) reply(op, 0x20120707); return; // SYNC
      case 0x0a: reply(op, u32(data, 0) === 0x40001000 ? magic : regs[u32(data, 0)] || 0); return; // READ_REG
      case 0x06: reply(op); send(new TextEncoder().encode("OHAI")); return; // MEM_END: the stub runs
      case 0x10: // FLASH_DEFL_BEGIN: uncompressed size, blocks, block size, offset
        defl = { size: u32(data, 0), offset: u32(data, 12), parts: [] };
        esp.begins.push({ offset: defl.offset, size: defl.size });
        reply(op); return;
      case 0x11: // FLASH_DEFL_DATA
        if (failWrite) { reply(op, 0, [1, 0x05]); return; }
        if (hold && !esp.held) { esp.held = true; await new Promise(r => { esp.release = r; }); }
        defl.parts.push(data.slice(16, 16 + u32(data, 0))); reply(op); return;
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

  // The app: its boot log, then Improv (firmware/main/improv.c) on a Wi-Fi board.
  const enc = new TextEncoder();
  const packet = (type, data) => {
    const p = [..."IMPROV"].map(ch => ch.charCodeAt(0)).concat([1, type, data.length], data);
    p.push(p.reduce((a, b) => (a + b) & 0xff, 0), 10);
    out(new Uint8Array(p));
  };
  const result = (cmd, strs) => {
    const d = strs.flatMap(s => { const b = [...enc.encode(s)]; return [b.length, ...b]; });
    packet(4, [cmd, d.length, ...d]);
  };
  let state = 2;
  const URL = "http://192.0.2.52/status";
  const rpc = (cmd, data) => {
    if (cmd === 2) { packet(1, [state]); if (state === 4) result(2, [URL]); }
    else if (cmd === 3) result(3, ["espDNS", "0.0.5", "esp32s3", "espdns-e2e"]);
    else if (cmd === 4) { result(4, ["example-net", "-48", "YES"]); result(4, ["example-guest", "-71", "NO"]); result(4, []); }
    else if (cmd === 1) {
      esp.join = window.__improvJoin = new TextDecoder().decode(new Uint8Array(data));
      packet(2, [0]); packet(1, [3]);
      if (join) { state = 4; packet(1, [4]); result(1, [URL]); } else { packet(2, [3]); packet(1, [2]); }
    } else packet(2, [2]);
  };
  let ibuf = [];
  const improv = chunk => {
    ibuf.push(...chunk);
    for (;;) {
      const i = ibuf.findIndex((_, k) => String.fromCharCode(...ibuf.slice(k, k + 6)) === "IMPROV");
      if (i < 0 || ibuf.length < i + 10 + ibuf[i + 8]) return;
      const d = ibuf.slice(i + 9, i + 9 + ibuf[i + 8]);
      ibuf = ibuf.slice(i + 10 + ibuf[i + 8]);
      if (ibuf[0] === 10) ibuf.shift();
      rpc(d[0], d.slice(2, 2 + d[1]));
    }
  };
  // keyLate: its first boot's node key line is missed (said before the console was read).
  let boots = 0;
  const boot = () => {
    if (!port.readable || esp.mode !== "app") return;
    booted = true;
    const lines = keyLate && !boots++ ? bootLog.filter(l => !l.includes("node key")) : bootLog;
    out(enc.encode(lines.map(l => `${l}\r\n`).join("")));
  };
  esp.print = lines => { if (port.readable && esp.mode === "app") out(enc.encode(lines.map(l => `${l}\r\n`).join(""))); };

  // The host's bytes: in the bootloader, SLIP frames, one command at a time; in the app,
  // Improv packets (a wired board's app reads nothing).
  const streams = () => {
    let frame = null, esc = false, queue = Promise.resolve();
    return {
      readable: new ReadableStream({ start(c) { ctrl = c; } }),
      writable: new WritableStream({
        write(chunk) {
          if (esp.mode === "app") { if (wifi) improv(chunk); return; }
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
      }),
    };
  };
  const port = {
    readable: null, writable: null,
    async open() {
      if (port.readable) throw new DOMException("The port is already open.", "InvalidStateError");
      esp.opened++;
      openedIn = esp.mode;
      Object.assign(port, streams());
      if (!booted) setTimeout(boot, 50); // its boot log, as a port opened after the restart reads it
    },
    async close() {
      if (!port.readable) throw new DOMException("The port is already closed.", "InvalidStateError");
      if (port.readable.locked || port.writable.locked) throw new TypeError("Cannot cancel a locked stream");
      port.readable = port.writable = null;
    },
    async setSignals(s) {
      esp.signals.push({ ...s, after: esp.ops.length });
      if (s.requestToSend === true && !rts) { rts = true; dtr = false; }
      if (s.dataTerminalReady === true && rts) dtr = true;
      if (s.requestToSend === false && rts) {
        rts = false;
        esp.mode = dtr ? "rom" : "app";
        booted = esp.mode === "rom";
        if (esp.mode === "app") {
          state = 2;
          if (port.readable && openedIn === "app") setTimeout(boot, 50);
        }
      }
    },
    async forget() {},
    getInfo() { return { usbVendorId: vid, usbProductId: vid === 0x303a ? 0x1001 : 0xea60 }; },
    addEventListener() {}, removeEventListener() {},
  };
  const serial = { async requestPort() { return port; }, async getPorts() { return [port]; }, addEventListener() {}, removeEventListener() {} };
  Object.defineProperty(Navigator.prototype, "serial", { configurable: true, get: () => serial });
}

module.exports = { fakeEsp };
