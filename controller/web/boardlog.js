// The board's side of Install over USB (usb.js), after the flash: the Board log (one row per
// line, colour codes stripped, capped, with Download log and Restart the board), the board's
// console read from its port at 115200, and Improv Wi-Fi serial in that same byte stream
// (firmware/main/improv.c: "IMPROV", version 1, type, length, data, checksum = the sum of
// the bytes before it, then a newline).
import { h } from "./shell.js";
import { save } from "./keys.js";

const MAX_ROWS = 5000;
const MAGIC = [..."IMPROV"].map(c => c.charCodeAt(0));
const sleep = ms => new Promise(r => setTimeout(r, ms));
// clean is a line as the log shows it: no carriage returns, no colour codes.
export const clean = l => l.replace(/\r/g, "").replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "");
export const IMPROV = { STATE: 1, ERROR: 2, RPC: 3, RESULT: 4, READY: 2, PROVISIONING: 3, PROVISIONED: 4, WIFI: 1, GET_STATE: 2, INFO: 3, SCAN: 4, NO_CONNECT: 3 };

// logView is the Board log: add(line) a row, clear(), and the restart button, enabled while
// there is a console to restart through (restart(), when pressed).
export function logView(fileName, restart) {
  const rows = h("div", { class: "log mono", id: "usb-log-rows", role: "log", tabindex: "0", "aria-label": "Board log" });
  const down = h("button", { class: "btn small", type: "button", id: "usb-log-save" }, "Download log");
  const again = h("button", { class: "btn small", type: "button", id: "usb-log-reset", disabled: true }, "Restart the board");
  const box = h("details", { class: "sub-ex", id: "usb-log" }, h("summary", { class: "small" }, "Board log"),
    h("div", { class: "stack" }, rows, h("div", { class: "row" }, down, again)));
  down.addEventListener("click", () => save([...rows.children].map(r => r.textContent).join("\n") + "\n", fileName(), "text/plain"));
  again.addEventListener("click", async () => {
    again.disabled = true;
    try { await restart(); } catch { /* the port went */ }
    again.disabled = false;
  });
  return {
    box,
    add(line) {
      const end = rows.scrollTop + rows.clientHeight >= rows.scrollHeight - 8;
      rows.append(h("div", {}, line));
      while (rows.childElementCount > MAX_ROWS) rows.firstElementChild.remove();
      if (end) rows.scrollTop = rows.scrollHeight;
    },
    clear() { rows.replaceChildren(); },
    restartable(on) { again.disabled = !on; },
  };
}

// openConsole opens the board's port at 115200 and reads it: each line to line(text), Improv
// frames to frame(type, data). After a restart the board's own USB may come back as a new
// device: the same port is tried for up to 10 s, and so is a port with its USB IDs that
// appeared since, only while it is the only one (others: the ports there before the install,
// another board's among them). It gives up, null, once live() says the install was stopped.
// It returns {send(type, data), restart(), close()}.
export async function openConsole(port, { line, frame, others = [], live = () => true }) {
  const info = port.getInfo();
  let p = null;
  for (let t = Date.now(); !p;) {
    if (!live()) return null;
    const come = (await navigator.serial.getPorts()).filter(q => q !== port && !others.includes(q) &&
      q.getInfo().usbVendorId === info.usbVendorId && q.getInfo().usbProductId === info.usbProductId);
    for (const q of [port, ...(come.length === 1 ? come : [])]) {
      try { await q.open({ baudRate: 115200 }); p = q; break; } catch (e) { if (Date.now() - t > 10000) throw e; }
    }
    if (!p) await sleep(500);
  }
  if (!live()) { try { await p.close(); } catch { /* gone */ } return null; }
  const reader = p.readable.getReader(), writer = p.writable.getWriter();
  const dec = new TextDecoder();
  let held = [], text = "", skipNL = false, closed = false;
  const lines = s => {
    text += s;
    const parts = text.split("\n");
    text = parts.pop();
    if (text.length > 4096) { parts.push(text); text = ""; }
    for (const l of parts) line(clean(l));
  };
  // feed takes the bytes read: Improv frames out, the rest as text. A possible frame start at
  // the end is held until the next bytes say.
  const feed = bytes => {
    const b = held.concat(Array.from(bytes)), out = [];
    let i = 0;
    for (; i < b.length;) {
      if (skipNL) { skipNL = false; if (b[i] === 10) { i++; continue; } }
      const n = Math.min(6, b.length - i);
      if (b[i] !== MAGIC[0] || MAGIC.slice(0, n).some((m, k) => b[i + k] !== m)) { out.push(b[i++]); continue; }
      if (b.length - i < 9 || b.length - i < 10 + b[i + 8]) break;
      const len = b[i + 8], f = b.slice(i, i + 10 + len);
      if (f[6] !== 1 || f.slice(0, -1).reduce((a, x) => (a + x) & 0xff, 0) !== f[9 + len]) { out.push(b[i++]); continue; }
      i += 10 + len;
      skipNL = true;
      frame(f[7], f.slice(9, 9 + len));
    }
    held = b.slice(i);
    lines(dec.decode(new Uint8Array(out), { stream: true }));
  };
  const done = (async () => {
    try {
      for (;;) {
        const { value, done: end } = await reader.read();
        if (end) break;
        feed(value);
      }
    } catch { /* closed, or unplugged */ }
  })();
  return {
    send(type, data) {
      const f = [...MAGIC, 1, type, data.length, ...data];
      f.push(f.reduce((a, x) => (a + x) & 0xff, 0));
      return writer.write(new Uint8Array(f));
    },
    // restart: RTS raised with DTR low (not into the bootloader), then dropped.
    async restart() {
      await p.setSignals({ dataTerminalReady: false, requestToSend: true });
      await sleep(100);
      await p.setSignals({ requestToSend: false });
    },
    async close() {
      if (closed) return;
      closed = true;
      try { await reader.cancel(); } catch { /* gone */ }
      await done;
      try { reader.releaseLock(); } catch { /* gone */ }
      try { writer.releaseLock(); } catch { /* gone */ }
      try { await p.close(); } catch { /* closed already */ }
    },
  };
}

// rpc is an Improv RPC frame's data: the command, then its data's length and the data.
export const rpc = (cmd, data = []) => [cmd, data.length, ...data];
// joinData is the Wi-Fi RPC's data: the network's name and the password, each with its length.
export function joinData(ssid, pass) {
  const e = new TextEncoder(), s = [...e.encode(ssid)], p = [...e.encode(pass)];
  return [s.length, ...s, p.length, ...p];
}
// result is a RESULT frame's command and strings ([cmd, length, (length, bytes)…]).
export function result(d) {
  const strs = [], dec = new TextDecoder();
  for (let i = 2; i < d.length; i += 1 + d[i]) strs.push(dec.decode(new Uint8Array(d.slice(i + 1, i + 1 + d[i]))));
  return { cmd: d[0], strs };
}
