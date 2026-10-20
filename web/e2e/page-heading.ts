import { expect, type Page } from "@playwright/test";
import { words } from "./words.ts";

// pageHeading waits for the heading of the page itself. Every state that replaces the page area carries a level-one
// heading while it loads (docs/frontend.md「工程要求」), so a wait that stops at any heading can stop at that state: what
// the page shows is the heading that is not the loading one's word (#363).
export async function pageHeading(page: Page) {
  const heading = page.getByRole("heading", { level: 1 });
  await heading.waitFor();
  await expect(heading).not.toHaveText(words("state.loading"), { timeout: 0 });
  return heading;
}
