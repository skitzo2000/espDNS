// The USB flasher (usb.js's Write keys only): the vendored esptool-js (web/vendor/esptool-js,
// esptool-js 0.6.0) on a Web Serial port the page asked for. connect reaches the board's
// serial bootloader and says which chip and how much flash; write writes parts ({data,
// address}) at their offsets, erasing the whole flash first only when asked; reset restarts
// the board into its firmware, as ESP Web Tools does; close lets go of the port. serialError
// says what went wrong, in words, for the step that failed.

let esptool = null;
const loadEsptool = () => (esptool ||= import("./vendor/esptool-js/bundle.js"));
const sleep = ms => new Promise(r => setTimeout(r, ms));

// connect opens the port and the bootloader (the flasher stub uploaded and run). log(line),
// if given, gets esptool-js's output. chip is the chip's name ("ESP32-S3"), description what
// the bootloader says of it (its package and revision too); flashMB is the flash chip's size
// as its ID says, or null when it doesn't say.
export async function connect(port, log) {
  const { ESPLoader, Transport } = await loadEsptool();
  const transport = new Transport(port, false);
  let line = "";
  const out = t => { line += t; };
  const terminal = {
    clean() {},
    write(t) { out(t); },
    writeLine(t) { out(t); if (log && line.trim()) log(line.trim()); line = ""; },
  };
  const loader = new ESPLoader({ transport, baudrate: 115200, terminal });
  let description;
  try {
    description = await loader.main();
  } catch (e) {
    await close(transport);
    throw e;
  }
  let flashMB = null;
  try {
    const size = loader.DETECTED_FLASH_SIZES[(await loader.readFlashId()) >> 16 & 0xff];
    if (size) flashMB = loader.flashSizeBytes(size) / 0x100000;
  } catch { /* unknown */ }
  return { loader, transport, chip: loader.chip.CHIP_NAME, description, flashMB };
}

// write writes parts, [{data: Uint8Array, address}], compressed. erase erases the whole flash
// first; without it nothing but the parts' own sectors is touched. progress(written, total)
// is per part.
export async function write(loader, parts, { erase = false, progress } = {}) {
  if (erase) await loader.eraseFlash();
  await loader.writeFlash({
    fileArray: parts,
    flashMode: "keep", flashFreq: "keep", flashSize: "keep",
    eraseAll: false, compress: true,
    reportProgress: (_, written, total) => { if (progress) progress(written, total); },
  });
}

// reset restarts the board into its firmware: RTS raised, then dropped (esptool-js's hard
// reset only drops it, which on a board whose RTS is already low restarts nothing).
export async function reset(transport, loader) {
  await transport.setRTS(true);
  await sleep(100);
  await loader.after("hard_reset");
}

// close lets go of the port, closed already or not.
export async function close(transport) {
  if (!transport) return;
  try { await transport.disconnect(); } catch { /* closed already */ }
}

// serialError is what went wrong, in words: e from step "port", "connect", "write", "verify"
// or "reset" (or another, said as it is). said[step](message), if given, says that step's
// failure in the caller's own words instead.
export function serialError(e, step, said = {}) {
  const m = String((e && e.message) || e);
  if (e && e.name === "NotFoundError") return "No port was chosen: nothing was written.";
  if (e && (e.name === "InvalidStateError" || e.name === "NetworkError") || /open/i.test(m) && /port/i.test(m)) {
    return "The board's port is busy: close the other tab or program using it (a serial monitor, another install) and try again.";
  }
  if (e && e.name === "SecurityError") return "The browser didn't allow the serial port: open this page on the controller's machine (localhost) in Chrome or Edge.";
  if (said[step]) return said[step](m);
  switch (step) {
    case "connect": return `Couldn't reach the board's bootloader (${m}). Unplug it, hold BOOT while you plug it back in, and try again.`;
    case "write": return `Writing failed (${m}). Try again.`;
    case "verify": return `What was read back doesn't match what was written (${m}). Try again.`;
    case "reset": return `It's written, but the board didn't restart (${m}): unplug it and plug it in again.`;
  }
  return m;
}
