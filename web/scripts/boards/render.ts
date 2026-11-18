import { chromium, type Page } from "@playwright/test";
import { type BoardScene, openBoard } from "../../e2e/design/board.ts";
import { fontsLoaded } from "../../e2e/design/fonts.ts";
import type { Stage } from "../../e2e/design/stage.ts";
import type { Region } from "../../packages/boards/regions.ts";

type Capture = { scene: BoardScene; view: Stage; cuts: { region: Region; root: string }[] };

function captures(regions: readonly Region[]) {
  const groups = new Map<string, Capture>();
  for (const region of regions) {
    const render = region.render;
    if (!render) continue;
    // A DOM cut waits for the faces used by the mounted board. Preloading every face is for canvas drawing.
    const scene = {
      file: `${region.board}.dc.html`,
      props: render.props,
      actions: render.actions,
      engines: [],
      preloadFonts: false,
    };
    const viewport = render.viewport ?? { width: 1440, height: 900 };
    const key = JSON.stringify([scene, viewport]);
    let capture = groups.get(key);
    if (!capture) {
      capture = {
        scene,
        view: {
          viewport,
          colorScheme: "light",
          timezoneId: "Asia/Shanghai",
          time: new Date("2026-10-24T04:00:00Z"),
        },
        cuts: [],
      };
      groups.set(key, capture);
    }
    capture.cuts.push({ region, root: render.root });
  }
  return [...groups.values()];
}

function captureMarkup(page: Page, root: string) {
  return page.locator(root).evaluate((element) => {
    let copy = element.cloneNode(true) as Element;
    // Ancestors participate in CSS selection but are not emitted unless roots selects them.
    for (let parent = element.parentElement; parent && parent.id !== "root"; parent = parent.parentElement) {
      const ancestor = parent.cloneNode(false) as Element;
      ancestor.append(copy);
      copy = ancestor;
    }
    for (const node of [copy, ...copy.querySelectorAll("*")]) {
      for (const name of node.getAttributeNames()) {
        if (/^(data-design-key|data-ref|data-event-.*)$/.test(name)) node.removeAttribute(name);
      }
    }
    return copy.outerHTML;
  });
}

// Settle each scene once and capture all its cuts. Separate contexts keep independent scenes deterministic.
export async function renderRegions(directory: string, regions: readonly Region[]): Promise<Map<Region, string>> {
  const pending = captures(regions);
  const result = new Map<Region, string>();
  const browser = await chromium.launch();
  try {
    const render = async () => {
      for (let capture = pending.pop(); capture; capture = pending.pop()) {
        const board = await openBoard(browser, capture.scene, directory, capture.view);
        try {
          await fontsLoaded(board.page, board.fonts);
          await board.page.clock.runFor(5000);
          const markup = new Map<string, string>();
          for (const { region, root } of capture.cuts) {
            let html = markup.get(root);
            if (html === undefined) {
              html = await captureMarkup(board.page, root);
              markup.set(root, html);
            }
            result.set(region, html);
          }
        } finally {
          await board.close();
        }
      }
    };
    // A bounded pool avoids scheduling every large artboard and its fonts at once.
    const jobs = await Promise.allSettled(Array.from({ length: Math.min(pending.length, 3) }, render));
    for (const job of jobs) if (job.status === "rejected") throw job.reason;
    return result;
  } finally {
    await browser.close();
  }
}
