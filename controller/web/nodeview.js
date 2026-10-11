// A node in words, shared by Nodes (home.js) and the node page (node.js): its state as its
// LED shows it, with the reason in words and what each reason means, how long it has been
// up, its link, the boards' titles, and the last hour's sparkline.
import { h, arr, obj, str } from "./shell.js";
import { getJSON } from "./common.js";

export const linkWord = k => (k === "ethernet" ? "Wired" : k === "wifi" ? "Wi-Fi" : "");
export const sentenceCase = s => (s ? s[0].toUpperCase() + s.slice(1) : s);

export function upFor(sec) {
  if (typeof sec !== "number" || sec < 0) return "";
  const d = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? `${d} d ${hh} h` : hh ? `${hh} h ${m} m` : `${m} m`;
}
export function sinceWords(t) {
  const s = Math.max(0, Math.round((Date.now() - new Date(t)) / 1000));
  return s < 90 ? `${s} s` : s < 5400 ? `${Math.round(s / 60)} min` : s < 129600 ? `${Math.round(s / 3600)} h` : `${Math.round(s / 86400)} days`;
}

// Reasons that don't make a node abnormal (docs/troubleshooting.md, Health reasons).
export const INFO = ["config on trial", "reboot pending", "service restarting"];

// What each health reason means, and its kind: bad (not answering), warn (degraded) or info
// (not degraded). Every reason firmware/main/health.c can report is here (reasons_test.go).
export const REASONS = {
  "listeners failed": ["bad", "A DNS listener failed to open."],
  "listeners stalled": ["bad", "The listeners stalled after 3 stall reboots in a row: the node stays up for the controller rather than boot-loop."],
  "no link": ["bad", "No Ethernet link, or not associated with the Wi-Fi network. Check its cable or its Wi-Fi."],
  "no address": ["bad", "The network interface has no IPv4 address."],
  "sd card": ["warn", "The board has a slot, but no card, or it failed or timed out mounting. Hosted zones and blocking are unavailable; secondary zones are kept in RAM only. Swap the card or the node."],
  blocking: ["warn", "An installed blocklist or the allowed and blocked sites failed to load, or a sector read failed."],
  "zone expired": ["warn", "A copied zone passed its expiry without a refresh from your primary: it no longer answers for it."],
  "zone refresh failing": ["warn", "A copied zone's checks or copies from your primary failed 3 times in a row. Check that your primary allows this node to copy the zone."],
  "forwarders failing": ["warn", "5 upstream queries in a row went unanswered over at least 30 s. Check its upstream DNS."],
  "forwarder task stalled": ["warn", "The task that asks the upstream DNS servers stalled: forwarded queries go unanswered, cached and local ones are still answered. The node doesn't restart for it."],
  "forwarders slow": ["warn", "Its upstream DNS servers are too slow for the queries it is asked: queries were turned away, or most took longer than the timeout. Cached and local answers aren't affected. Check its upstream DNS."],
  overloaded: ["warn", "More DNS queries come than it can answer: over the last 30 s, at least a tenth of the queries it received (and at least 64) were dropped because every worker was busy and its queue full, or waited over 1 s in the queue and were dropped then; or 64 waited that long. It answers what it can, cached and local answers included; clients ask again or ask another server. Spread the load over more nodes, or find the client that floods it."],
  "low memory": ["warn", "Under 16 KB of internal memory free."],
  "no board definition": ["warn", "No board definition: the node runs on its firmware's defaults."],
  config: ["warn", "Its stored settings aren't in use (refused, unreadable, failed their trial, or don't fit the board's memory): it runs on its older settings or the firmware's defaults."],
  "hosted zones": ["warn", "The hosted zones failed to load, or a zone in them isn't served because its settings name it too."],
  "service failed": ["warn", "A service failed to start 3 times in a row, or stopped responding."],
  "older copy": ["warn", "The newest blocklist, allowed and blocked sites or hosted zones on its card are damaged or too big: it runs the older copy it kept. Apply the change again."],
  "no keys": ["warn", "Setup mode: the node has no keys to check releases with (its keys partition is empty or damaged, and its firmware has none built in). It answers DNS on its board's defaults and refuses every update and setting. Install it again with Install over USB, or write its keys over USB."],
  "config on trial": ["info", "New settings wait to be confirmed; if the node isn't reached on them, it goes back to its previous settings."],
  "reboot pending": ["info", "A change waits for a restart, which the controller makes only while another node answers."],
  "service restarting": ["info", "A service that failed to start is being started again."],
  degraded: ["warn", "Old firmware: it says only that it is degraded. Update it."],
};

// reason is one reason's kind and meaning: only REASONS' own entries ("__proto__" from a
// node is unknown, not Object's).
export function reason(r) {
  return typeof r === "string" && Object.hasOwn(REASONS, r) ? REASONS[r] : ["warn", "A reason this controller doesn't know yet."];
}

// stateOf is a node's state as its LED shows it: the LED pattern, a word, why, and the row's
// tint ("" normal, warn, bad).
export function stateOf(n) {
  const s = obj(n.status), hh = obj(s.health);
  // Out of service: muted, never an alert, whatever it last said; why says if it answers.
  if (n.out_of_service) {
    const why = !n.online ? (n.status ? `Not answering for ${sinceWords(n.last_seen)}` : "Not answering") : "Answering, sent no changes";
    return { led: "out", word: "Out of service", why, tint: "", out: true };
  }
  if (!n.online) {
    const why = n.status ? `No reply for ${sinceWords(n.last_seen)}` : "No reply since the controller started";
    return { led: "fault", word: "Not answering", why, tint: "bad" };
  }
  const reasons = arr(hh.reasons).filter(r => typeof r === "string");
  const words = rs => rs.map(sentenceCase).join(", ");
  const st = hh.state;
  if (st === undefined || st === null || st === "") {
    // Firmware from before health states: its degraded flag.
    return s.degraded ? { led: "unknown", word: "Degraded", why: "Old firmware: update it", tint: "warn" }
      : { led: "unknown", word: "Answering", why: "Old firmware: update it", tint: "" };
  }
  switch (st) {
    case "healthy": return { led: "healthy", word: "Healthy", why: words(reasons.filter(r => INFO.includes(r))), tint: "" };
    case "degraded": return { led: "degraded", word: "Degraded", why: words(reasons) || "Something it runs failed", tint: "warn" };
    case "no network": return { led: "nonet", word: "No network", why: words(reasons), tint: "bad" };
    case "fault": return { led: "fault", word: "Fault", why: words(reasons), tint: "bad" };
    case "updating": return { led: "updating", word: "Updating", why: "Receiving new firmware", tint: "" };
    case "booting": return { led: "booting", word: "Starting", why: "", tint: "" };
    default: return { led: "unknown", word: typeof st === "string" ? sentenceCase(st) : "Unknown state", why: words(reasons), tint: "bad" };
  }
}

// readBoards reads the board catalog's titles (/api/boards) into a map, name -> title.
export async function readBoards(into) {
  try {
    for (const b of arr(await getJSON("api/boards"))) { const bd = obj(b.board); if (bd.name) into.set(bd.name, str(bd.title) || bd.name); }
  } catch { /* names, then */ }
  return into;
}

// spark is a sparkline of the values (null for a minute with none); null with under two.
export function spark(values) {
  const vs = arr(values).map(v => (typeof v === "number" && isFinite(v) ? v : null));
  const known = vs.filter(v => v !== null);
  if (known.length < 2) return null;
  const max = Math.max(...known) || 1, w = 200, hgt = 50;
  const pts = vs.map((y, i) => (y === null ? null : `${(i / (vs.length - 1) * w).toFixed(1)},${(hgt - 4 - y / max * (hgt - 10)).toFixed(1)}`)).filter(Boolean).join(" ");
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  for (const [k, v] of Object.entries({ class: "spark", viewBox: `0 0 ${w} ${hgt}`, preserveAspectRatio: "none", "aria-hidden": "true" })) svg.setAttribute(k, v);
  const first = pts.split(" ")[0].split(",")[0], last = pts.split(" ").at(-1).split(",")[0];
  const poly = document.createElementNS(NS, "polygon");
  poly.setAttribute("points", `${first},${hgt} ${pts} ${last},${hgt}`);
  const line = document.createElementNS(NS, "polyline");
  line.setAttribute("points", pts);
  line.setAttribute("vector-effect", "non-scaling-stroke");
  svg.append(poly, line);
  return svg;
}

// mini is one of the last hour's small charts: a title, a value and a sparkline.
export const mini = (t, v, series, title) => h("div", { class: "panel mini", title }, h("span", { class: "small muted" }, t), h("span", { class: "v num" }, v), spark(series));

export const fmtNum = v => (typeof v === "number" && isFinite(v) ? (v < 10 ? v.toFixed(1) : Math.round(v).toLocaleString()) : "–");
export const pct = v => (typeof v === "number" && isFinite(v) ? `${(v * 100).toFixed(v < 0.1 ? 1 : 0)}%` : "–");
export const andList = xs => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);
