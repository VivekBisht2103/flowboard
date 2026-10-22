import { expect, test, type Page } from "@playwright/test";

// Each test starts from the seeded workspace. The API exposes no reset endpoint
// (by design), so specs only make changes they can tolerate on re-run, or the
// runner reseeds first: `docker compose run --rm seed --reset` (see README).

const column = (page: Page, key: string) => page.getByTestId(`column-${key}`);

test("sibling lists render different kanban columns", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("tree").getByRole("button", { name: "Backlog", exact: true }).click();
  await expect(page.locator(".column h3")).toHaveText(["To Do", "In Progress", "In Review", "Done"]);
  await page.getByRole("tree").getByRole("button", { name: "Sprint 12", exact: true }).click();
  await expect(page.locator(".column h3")).toHaveText(["Ready", "Blocked", "In Progress", "QA", "Done"]);
});

test("select list → open task → edit a custom field → save → persisted", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("tree").getByRole("button", { name: "Sprint 12", exact: true }).click();
  await page.getByLabel("Open Search indexer worker").click();
  const points = page.getByLabel("Story Points");
  const next = String(Number(await points.inputValue()) + 1);
  await points.fill(next);
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.locator(".toast")).toContainText("Saved");
  await page.reload();
  await page.getByRole("tree").getByRole("button", { name: "Sprint 12", exact: true }).click();
  await page.getByLabel("Open Search indexer worker").click();
  await expect(page.getByLabel("Story Points")).toHaveValue(next);
});

test("drag a card to another column updates its status", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("tree").getByRole("button", { name: "Sprint 12", exact: true }).click();
  const card = page.getByLabel("Open Terraform state lock contention");
  const from = (await column(page, "blocked").locator(".card-slot").count()) > 0 ? "blocked" : "in_progress";
  const to = from === "blocked" ? "in_progress" : "blocked";
  const cb = (await card.boundingBox())!;
  const tb = (await column(page, to).locator(".column-body").boundingBox())!;
  await page.mouse.move(cb.x + 20, cb.y + 20);
  await page.mouse.down();
  await page.mouse.move(cb.x + 40, cb.y + 40, { steps: 5 });
  await page.mouse.move(tb.x + 40, tb.y + tb.height - 8, { steps: 15 });
  await page.mouse.up();
  await expect(column(page, to)).toContainText("Terraform state lock contention");
  await page.reload();
  await page.getByRole("tree").getByRole("button", { name: "Sprint 12", exact: true }).click();
  await expect(column(page, to)).toContainText("Terraform state lock contention");
});

test("switching Alice → Bob hides the private Security Audit list", async ({ page }) => {
  await page.goto("/");
  const tree = page.getByRole("tree");
  await expect(tree.getByText("Security Audit")).toBeVisible();
  await page.getByLabel("Acting as").selectOption("bob");
  await expect(tree.getByText("Security Audit")).toHaveCount(0);
  await expect(tree.getByText("Content Calendar")).toBeVisible(); // Bob's grant on Marketing
  await page.getByLabel("Acting as").selectOption("carol");
  await expect(tree.getByText("Security Audit")).toBeVisible();
  await expect(tree.getByText("Backlog")).toHaveCount(0); // explicit deny
  await page.getByLabel("Acting as").selectOption("alice");
});
