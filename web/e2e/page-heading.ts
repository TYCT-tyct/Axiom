import { expect, type Page } from "@playwright/test";
import { words } from "./words.ts";

// How long a page area is given to come up before the check says what it showed instead. Not a bound: the check's own
// timeout is what fails it, and this only says why.
const explainAfter = 5_000;

// What the page area holds, for a check waiting on a page that has not come up: the document's state, the headings in
// it, whether a state is still loading, what is alerting, and the start of the text — which says whether the app started
// at all, the shape #363 took.
async function pageState(page: Page) {
  return page
    .evaluate(() => ({
      ready: document.readyState,
      headings: [...document.querySelectorAll("h1")].map((node) => node.textContent?.trim() ?? ""),
      loading: document.querySelectorAll("[role=status]").length,
      alerts: [...document.querySelectorAll("[role=alert]")].map((node) => node.textContent?.trim().slice(0, 80) ?? ""),
      main: document.querySelectorAll("main").length,
      text: document.body.innerText.replace(/\s+/g, " ").slice(0, 120),
    }))
    .catch((error: unknown) => `page unavailable: ${String(error)}`);
}

async function explain(page: Page, name: string, asked?: () => unknown) {
  const shown: unknown = await pageState(page);
  console.warn(
    `[e2e] ${name}: still not showing its page ${explainAfter} ms on, the document showed ${JSON.stringify(shown)}` +
      (asked ? `; requests: ${JSON.stringify(asked())}` : ""),
  );
}

// pageHeading waits for the heading of the page itself: every state that replaces the page area carries a level-one
// heading while it loads (docs/frontend.md「工程要求」), so a wait that stops at any heading can stop at that state. The
// heading the page shows is the one that is not the loading state's word (#363). `asked` adds what the API was asked,
// which says whether a page that is not there asked anything at all.
export async function pageHeading(page: Page, name: string, asked?: () => unknown) {
  const heading = page.getByRole("heading", { level: 1 });
  const timer = setTimeout(() => void explain(page, name, asked).catch(() => {}), explainAfter);
  try {
    await heading.waitFor();
    await expect(heading).not.toHaveText(words("state.loading"), { timeout: 0 });
  } finally {
    clearTimeout(timer);
  }
  return heading;
}
