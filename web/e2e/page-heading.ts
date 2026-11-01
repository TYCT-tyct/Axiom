import { expect, type Page } from "@playwright/test";
import { words } from "./words.ts";

// pageHeading waits for the heading of the page itself, not the heading a state that replaces the page area gives it
// while it waits. Every such state has a level-one heading (docs/frontend.md「工程要求」), so stopping at any heading
// would stop at the loading state; the page's own heading is the one that is not the loading word (#363).
export async function pageHeading(page: Page) {
  const heading = page.getByRole("heading", { level: 1 });
  await heading.waitFor();
  await expect(heading).not.toHaveText(words("state.loading"), { timeout: 0 });
  return heading;
}
