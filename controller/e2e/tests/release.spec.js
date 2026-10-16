// Get firmware (firmware.js, firmware.go): chip images of the project's release fetched,
// checked with the project's key and imported, from Install over USB (a board whose chip
// image isn't in the controller yet: that one only) and from the account panel (those the
// listed nodes need: here there are none, so nothing), each image labelled "project release
// v…" or "your build". serve.sh's controller fetches from its stand-in forge (fakeforge, on
// 127.0.0.1: a release v0.0.9 signed by a throwaway key, its -project-pub), never the
// project's. Named to run after gui.spec.js, which expects no chip image imported yet.
const { test, expect } = require("@playwright/test");
const { login, apiGet } = require("./session");

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

// The fetch-release jobs the page starts, by their params.
function jobsStarted(page) {
  const params = [];
  page.on("request", r => {
    if (r.method() === "POST" && new URL(r.url()).pathname.endsWith("/api/jobs")) {
      const b = r.postDataJSON();
      if (b && b.kind === "fetch-release") params.push(b.params);
    }
  });
  return params;
}

test("The account panel before any firmware: no nodes and no chip images, so Get firmware has nothing to get", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await page.goto("./");
  await page.getByRole("button", { name: `Account: ${process.env.E2E_USER}` }).click();
  const d = page.getByRole("dialog", { name: process.env.E2E_USER });
  await d.locator("#acct-firmware > summary").click();
  await expect(d.locator("#fw-images")).toContainText("No firmware yet");
  await d.locator("#fw-get").click();
  await expect(d.locator("#fw-status")).toHaveText("Nothing to get yet: pick a board in Install over USB, and it gets that board's firmware.");
  await expect(d.locator("#fw-get")).toBeEnabled();
  const fw = await apiGet(page, "api/firmware");
  expect(fw.json.images).toEqual([]);
  expect(await violations()).toEqual([]);
});

test("Install over USB: Get firmware fetches the board's chip image only, and the board says which firmware it gets", async ({ page }) => {
  const violations = await watch(page);
  const started = jobsStarted(page);
  await login(page);
  await page.goto("./");
  await page.getByRole("button", { name: "Install over USB", exact: true }).click();
  const d = page.getByRole("dialog", { name: "Install a board over USB" });
  await expect(d).toBeVisible();
  await d.locator("#usb-board").selectOption("ws-s3-eth");
  const none = d.locator("#usb-noimage");
  await expect(none).toContainText("isn't in the controller yet");
  await expect(d.locator("#usb-fw-from")).toContainText("checked with the project's release key");
  await expect(d.locator("#usb-fw-from")).toContainText("/owner/espdns");
  await expect(d.locator("#usb-fw-images")).toContainText("No firmware yet");
  await d.locator("#usb-fw-get").click();
  // Imported: the board's facts now say which firmware it gets, and the box is gone
  await expect(d.locator("#usb-noimage")).toHaveCount(0, { timeout: 20000 });
  await expect(d.locator("#usb-facts")).toContainText("project release v0.0.9");
  const fw = await apiGet(page, "api/firmware");
  expect(fw.status).toBe(200);
  expect(fw.json.images.map(i => [i.image, i.origin, i.label])).toEqual([
    ["esp32s3-eth", "project", "project release v0.0.9"],
  ]);
  expect(started).toEqual([{ images: ["esp32s3-eth"] }]);
  expect(await violations()).toEqual([]);
});

test("The account panel: the firmware there, labelled; Get firmware gets what the nodes need, here nothing", async ({ page }) => {
  const violations = await watch(page);
  const started = jobsStarted(page);
  await login(page);
  await page.goto("./");
  await page.getByRole("button", { name: `Account: ${process.env.E2E_USER}` }).click();
  const d = page.getByRole("dialog", { name: process.env.E2E_USER });
  await expect(d).toBeVisible();
  await expect(d.locator("#fw-val")).toHaveText("project release v0.0.9");
  await d.locator("#acct-firmware > summary").click();
  const imgs = d.locator("#fw-images .change");
  await expect(imgs).toHaveCount(1);
  await expect(imgs.nth(0)).toContainText("esp32s3-eth");
  await expect(imgs.nth(0)).toContainText("project release v0.0.9");
  await d.locator("#fw-get").click();
  // No nodes listed: nothing needed, the chip image it has stays as it is
  await expect(d.locator("#fw-status")).toHaveText("Nothing to get yet: pick a board in Install over USB, and it gets that board's firmware.");
  await expect(d.locator("#fw-get")).toBeEnabled();
  await expect(imgs).toHaveCount(1);
  expect(started).toEqual([{}]);
  expect(await violations()).toEqual([]);
});
