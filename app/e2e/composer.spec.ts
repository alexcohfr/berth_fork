import { expect, mockOnly, test } from "./fixtures";
import { fakeAgent } from "./fake-agent";

// The reply box at the foot of a chat.

test.beforeEach(async ({ app }) => {
  await app.open({ params: { view: "conversation" } });
});

test("OpenCode providers collapse, stay open through refresh, and launch the selected variant", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  try {
    await page.route(`${agent.url}/v1/boxes/devl/api/info`, (route) => route.fulfill({ json: {
      name: "devl", version: "dev", tools: ["opencode"], capabilities: [],
      agents: [{ id: "opencode", name: "OpenCode", command: "opencode", model_flag: "--model" }],
    } }));
    let attempts = 0;
    const models = [{ id: "acme/coder", name: "Acme Coder", variants: ["low", "high"] }];
    await page.route(`${agent.url}/v1/boxes/devl/api/agents/opencode/models?*`, (route) => {
      expect(new URL(route.request().url()).searchParams.get("at")).toBe("shop");
      return ++attempts === 1
        ? route.fulfill({ status: 503, json: { error: "Catalog temporarily unavailable" } })
        : route.fulfill({ json: models });
    });
    let launched: Record<string, unknown> | undefined;
    await page.route(`${agent.url}/v1/boxes/devl/api/tasks`, (route) => {
      launched = route.request().postDataJSON();
      return route.fulfill({ status: 400, json: { error: "Test stopped before launch" } });
    });
    await page.clock.install();
    await app.open({ agent });
    const composer = page.getByTestId("task-composer");
    await composer.getByRole("button", { name: "Agents: OpenCode", exact: true }).click();
    await page.getByRole("menuitem", { name: /^OpenCode/ }).hover();
    await expect(page.getByRole("alert").filter({ hasText: "Catalog temporarily unavailable" })).toBeVisible();
    await page.getByRole("menuitem", { name: "Retry loading models" }).click();
    const provider = page.getByRole("menuitem", { name: "Acme", exact: true });
    await expect(provider).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByRole("menuitem", { name: "acme/coder", exact: true })).toHaveCount(0);
    await provider.click();
    await expect(provider).toHaveAttribute("aria-expanded", "true");
    models.push({ id: "second/coder", name: "Second Coder", variants: [] });
    await page.clock.fastForward(30_100);
    await expect(page.getByRole("menuitem", { name: "Second", exact: true })).toBeVisible();
    models.push({ id: "third/coder", name: "Third Coder", variants: [] });
    await page.getByRole("menuitem", { name: "Refresh models", exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "Third", exact: true })).toBeVisible();
    await expect(provider).toHaveAttribute("aria-expanded", "true");
    await provider.click();
    await expect(provider).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByRole("menuitem", { name: "acme/coder", exact: true })).toHaveCount(0);
    await provider.press("Enter");
    await expect(provider).toHaveAttribute("aria-expanded", "true");
    await page.getByRole("group", { name: "Acme", exact: true }).getByRole("menuitem", { name: "acme/coder", exact: true }).hover();
    await page.getByRole("menuitemcheckbox", { name: "High", exact: true }).click();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await expect(composer.getByRole("button", { name: "Agents: OpenCode · acme/coder#high", exact: true })).toBeVisible();
    const closedCalls = attempts;
    await page.clock.fastForward(60_100);
    expect(attempts).toBe(closedCalls);
    await composer.getByRole("textbox", { name: "What should your agents work on?" }).fill("Check the retry logic");
    await composer.getByRole("button", { name: "Start", exact: true }).click();
    await expect.poll(() => launched).toMatchObject({ agent: "opencode", model: "acme/coder#high", location: "shop" });
  } finally {
    await agent.close();
  }
});

test("a long reply is capped in height and scrolls, with Send in reach", async ({ app }) => {
  mockOnly();
  await app.openWorktree("devl/search-perf");
  const box = app.composer.getByRole("textbox", { name: "Reply" });
  await box.fill(Array.from({ length: 200 }, (_, i) => `line ${i + 1} of a very long pasted prompt`).join("\n"));
  await expect.poll(() => box.evaluate((el) => el.scrollHeight > el.clientHeight + 100)).toBe(true);
  const r = await box.evaluate((el) => ({ h: el.getBoundingClientRect().height, overflow: getComputedStyle(el).overflowY, cap: Math.min(innerHeight * 0.4, 16 * parseFloat(getComputedStyle(document.documentElement).fontSize)) }));
  expect(r.overflow).toBe("auto");
  expect(r.h).toBeLessThanOrEqual(r.cap + 1);
  const send = app.composer.getByRole("button", { name: "Send" });
  await expect(send).toBeEnabled();
  await expect(send).toBeInViewport({ ratio: 1 });
  // The caret's end is reachable: the field scrolls to its last line.
  await box.evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect.poll(() => box.evaluate((el) => Math.ceil(el.scrollTop + el.clientHeight) >= el.scrollHeight - 1)).toBe(true);
});

test("a pasted image becomes a chip with its size", async ({ app }) => {
  mockOnly("uploads to a box");
  await app.openWorktree("devl/search-perf");
  const box = app.composer.getByRole("textbox", { name: "Reply" });
  await box.click();
  // A screenshot on the clipboard, pasted as the app gets it from macOS.
  await box.evaluate(async (el) => {
    const c = document.createElement("canvas");
    c.width = 96;
    c.height = 64;
    const g = c.getContext("2d")!;
    g.fillStyle = "#3b82f6";
    g.fillRect(0, 0, 96, 64);
    g.fillStyle = "#f59e0b";
    g.fillRect(24, 16, 48, 32);
    const blob = await new Promise<Blob>((r) => c.toBlob((b) => r(b!), "image/png"));
    const dt = new DataTransfer();
    dt.items.add(new File([blob], "image.png", { type: "image/png" }));
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
  });
  const chip = app.composer.getByTestId("attachment-chip");
  await expect(chip).toHaveCount(1);
  // A screenshot has no name of its own: it is named for when it was pasted.
  await expect(chip).toContainText(/pasted-\d+\.png/);
  // Uploaded: the chip says how big it is, and Send takes it.
  await expect(chip).not.toHaveAttribute("data-state", /uploading|shrinking|error/);
  await expect(chip).toHaveText(/^pasted-\d+\.png\s*\d+ (B|KB)$/);
  await expect(chip.locator("img")).toBeVisible();
  await expect(app.composer.getByRole("button", { name: "Send" })).toBeEnabled();
});
