// Get firmware (firmware.js, firmware.go): the project's release fetched, checked with the
// project's key and imported, from Install over USB (a board whose chip image isn't in the
// controller yet) and from the account panel, each image labelled "project release v…"
// or "your build". serve.sh's controller fetches from its stand-in forge (fakeforge, on
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

test("Install over USB: Get firmware fetches the project's release, and the board says which firmware it gets", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await page.goto("./");
  await page.getByRole("button", { name: "+ Add node" }).click();
  await page.locator("#nqres").getByRole("button", { name: "Start" }).click();
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
    ["esp32p4-rev1", "project", "project release v0.0.9"],
    ["esp32s3-eth", "project", "project release v0.0.9"],
    ["esp32s3-octal", "project", "project release v0.0.9"],
  ]);
  expect(await violations()).toEqual([]);
});

test("The account panel: the firmware there, labelled, and Get firmware again", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await page.goto("./");
  await page.getByRole("button", { name: `Account: ${process.env.E2E_USER}` }).click();
  const d = page.getByRole("dialog", { name: process.env.E2E_USER });
  await expect(d).toBeVisible();
  await expect(d.locator("#fw-val")).toHaveText("project release v0.0.9");
  await d.locator("#acct-firmware > summary").click();
  const imgs = d.locator("#fw-images .change");
  await expect(imgs).toHaveCount(3);
  await expect(imgs.nth(0)).toContainText("esp32p4-rev1");
  await expect(imgs.nth(0)).toContainText("project release v0.0.9");
  await d.locator("#fw-get").click();
  await expect(d.locator("#fw-status")).toHaveText(/^Imported project release v0\.0\.9: esp32p4-rev1, esp32s3-eth, esp32s3-octal\.$/, { timeout: 20000 });
  await expect(d.locator("#fw-get")).toBeEnabled();
  expect(await violations()).toEqual([]);
});
