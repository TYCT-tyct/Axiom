import { chromium } from "@playwright/test";
import { openBoard } from "../../e2e/design/board.ts";
import { fontsLoaded } from "../../e2e/design/fonts.ts";
import type { Region } from "../../packages/boards/regions.ts";

// Match the mechanical board proofs: frozen clock, seeded randomness, pinned source fonts, no network in the page.
export async function renderRegions(directory: string, regions: readonly Region[]): Promise<Map<Region, string>> {
  const result = new Map<Region, string>();
  const browser = await chromium.launch();
  try {
    for (const region of regions) {
      const scene = region.render;
      if (!scene) continue;
      const board = await openBoard(
        browser,
        {
          file: `${region.board}.dc.html`,
          props: scene.props,
          actions: scene.actions,
          engines: [],
          preloadFonts: false,
        },
        directory,
        {
          viewport: scene.viewport ?? { width: 1440, height: 900 },
          colorScheme: "light",
          timezoneId: "Asia/Shanghai",
          time: new Date("2026-10-24T04:00:00Z"),
        },
      );
      try {
        await fontsLoaded(board.page, board.fonts);
        await board.page.clock.runFor(5000);
        const root = board.page.locator(scene.root);
        await root.waitFor({ state: "attached" });
        result.set(
          region,
          await root.evaluate((element) => {
            let copy = element.cloneNode(true) as Element;
            // Ancestors participate in CSS selection but are not emitted unless roots selects them.
            for (let parent = element.parentElement; parent && parent.id !== "root"; parent = parent.parentElement) {
              const ancestor = parent.cloneNode(false) as Element;
              ancestor.append(copy);
              copy = ancestor;
            }
            for (const node of [copy, ...copy.querySelectorAll("*")]) {
              node
                .getAttributeNames()
                .filter((name) => /^(data-design-key|data-ref|data-event-.*)$/.test(name))
                .forEach((name) => {
                  node.removeAttribute(name);
                });
            }
            return copy.outerHTML;
          }),
        );
      } finally {
        await board.close();
      }
    }
    return result;
  } finally {
    await browser.close();
  }
}
