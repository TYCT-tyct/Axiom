import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { type Browser, expect, type Page, type TestInfo, test } from "@playwright/test";
import { adminUrl, apps } from "./apps.ts";
import { type BoardScene, openBoard } from "./design/board.ts";
import { type Comparison, compare, shot } from "./design/compare.ts";
import { fontsLoaded, pageFonts } from "./design/fonts.ts";
import { rulings } from "./design/rulings.ts";
import { type Stage, settle, stage } from "./design/stage.ts";
import { type DesignScene, demoTime, designScenes } from "./design-map.ts";

// The pages against the design boards (docs/frontend.md「截图对比」), for the writer of a screen to check their work and
// for the reviewer to judge only the differences. For each design scene (design-map.ts), viewport width and colour
// scheme it writes board.png, page.png and, when they differ, diff.png into AXIOM_DESIGN_OUT/<scene>/, and the share of
// differing pixels and the rulings applied into AXIOM_DESIGN_OUT/summary.json. A scene fails when its shots differ
// beyond anti-aliasing outside the regions design/rulings.ts excuses. Only for development: the boards are not in the
// repository, so without AXIOM_DESIGN_DIR every test here is skipped and CI runs none.
//   AXIOM_DESIGN_DIR=<design project> AXIOM_DESIGN_OUT=/tmp/design pnpm --dir web e2e design.spec -g "console keys"

const directory = process.env.AXIOM_DESIGN_DIR as string;
test.skip(!directory, "set AXIOM_DESIGN_DIR to the design project to compare the pages with its boards");

// A comparison renders two documents and loads the unsubset source fonts (about 30 MB of Chinese), about 15 s on a
// shared 8-core machine; the default 30 s leaves no room for its load.
test.describe.configure({ timeout: 90_000 });

const timezoneId = "Asia/Shanghai";
test.use({ timezoneId });

// The apps are stamped with the version of the build under test and ask the API for it to notice a deployment; the
// mocks answer AXIOM_VERSION. Read the stamp from the build so no page shows the "Axiom has been updated" banner
// (the same as shots.spec.ts).
if (directory && !process.env.AXIOM_VERSION) {
  const html = await (await fetch(adminUrl())).text();
  const entry = html.match(/<script type="module"[^>]*src="([^"]+)"/)?.[1];
  const stamp = entry && (await (await fetch(adminUrl(entry))).text()).match(/axiom v[^"'`]+/)?.[0];
  if (!stamp) {
    throw new Error("the admin build carries no version stamp: run just web-embed first");
  }
  process.env.AXIOM_VERSION = stamp;
}

const view = (width: number, height: number, colorScheme: "light" | "dark"): Stage => ({
  viewport: { width, height },
  colorScheme,
  timezoneId,
  time: demoTime,
});

// The board side, rendered, staged and settled; the caller closes it.
async function readyBoard(browser: Browser, board: BoardScene, staged: Stage) {
  const opened = await openBoard(browser, board, directory, staged);
  await fontsLoaded(opened.page, opened.fonts);
  await settle(opened.page, staged.time);
  return opened;
}

// The page side: the scene's mocks and session, opened, with no request pending, its fonts loaded and settled.
async function readyPage(page: Page, design: DesignScene, staged: Stage) {
  const scene = design.suite.scenes.find((s) => s.name === design.scene);
  if (!scene) throw new Error(`${design.suite.app} has no scene ${design.scene}`);
  await page.setViewportSize(staged.viewport);
  await page.emulateMedia({ colorScheme: staged.colorScheme });
  await stage(page, staged);
  await design.suite.prepare(page, scene);
  await scene.open(page);
  await page.waitForLoadState("networkidle");
  await fontsLoaded(page, pageFonts(design.fonts));
  await settle(page, staged.time);
}

// record adds a comparison to summary.json. The tests of this file run one after another in one worker, and a failed
// test restarts the worker, so the file, not memory, carries the summary from one to the next.
function record(key: string, comparison: Comparison & { blocked: string[] }) {
  const file = join(dirname(dirname(comparison.board)), "summary.json");
  const summary = existsSync(file) ? JSON.parse(readFileSync(file, "utf8")) : {};
  const { error: _, ...kept } = comparison;
  summary[key] = kept;
  writeFileSync(file, `${JSON.stringify(summary, null, 2)}\n`);
}

for (const design of designScenes) {
  for (const [width, board] of Object.entries(design.boards)) {
    for (const scheme of ["light", "dark"] as const) {
      const key = `${design.name.replaceAll(" ", "-")}-${width}-${scheme}`;
      test(`${design.name} ${width} ${scheme}`, async ({ browser, page }, info) => {
        const staged = view(Number(width), board.height, scheme);
        const opened = await readyBoard(browser, { ...board, props: design.schemes[scheme] }, staged);
        try {
          await readyPage(page, design, staged);
          const used = rulings.filter((ruling) => ruling.scene === design.name);
          const comparison = await compare(opened.page, page, used, key, info);
          record(key, { ...comparison, blocked: opened.blocked });
          expect(comparison.error, `see ${comparison.diff ?? comparison.page}`).toBeUndefined();
        } finally {
          await opened.close();
        }
      });
    }
  }
}

// The tool itself, on boards written here: what the comparisons above rest on.
test.describe("the tool", () => {
  const [design] = designScenes as [DesignScene];
  const staged = view(1440, 900, "light");

  test("draws the same pixels on each run of a side", async ({ browser }, info) => {
    test.setTimeout(180_000); // two runs of each side
    const board = { ...(design.boards[1440] as BoardScene), props: design.schemes.light };
    const boards: Buffer[] = [];
    const pages: Buffer[] = [];
    for (const _ of [1, 2]) {
      const opened = await readyBoard(browser, board, staged);
      boards.push(await shot(opened.page));
      await opened.close();
      // A context of its own per run, as the page fixture is: the clock belongs to the context.
      const context = await browser.newContext({ baseURL: apps[1].url, locale: "zh-CN", timezoneId });
      const page = await context.newPage();
      await readyPage(page, design, staged);
      pages.push(await shot(page));
      await context.close();
    }
    expect(pages[0]?.equals(pages[1] as Buffer)).toBe(true);
    // The board is byte for byte the same on most runs; on about one in three, Chromium anti-aliases the corners of the
    // console overview's 30px status circle three levels apart (3 pixels), with the clock, animations and raster path
    // fixed. Playwright's comparison with no differing pixel allowed, at its per-pixel colour threshold, holds on all.
    const first = info.snapshotPath("tool-determinism", "board.png");
    mkdirSync(dirname(first), { recursive: true });
    writeFileSync(first, boards[0] as Buffer);
    expect(boards[1]).toMatchSnapshot(["tool-determinism", "board.png"], { maxDiffPixels: 0 });
  });

  // A board of the design tool's form: its font CSS, a 40px icon whose colour is a prop, a list drawn by a loop that
  // two methods change, and a script that reaches for the network.
  const usualFonts = "@font-face{font-family:'Axiom Sans';font-weight:400;src:url(/_blob/0)} p{font:32px 'Axiom Sans'}";
  const fixture = (fonts: string) => `<x-dc><helmet><style>
${fonts} p{margin:0} .icon{width:40px;height:40px} .a{background:#000} .b{background:#fff}
</style></helmet><div class="board"><p>Axiom</p><p class="icon {{icon}}"></p>
<ul><sc-for list="{{items}}" as="item"><li>{{item}}</li></sc-for></ul></div></x-dc>
<script type="text/x-dc" data-dc-script data-props='{"icon":{"default":"a"}}'>
class Component extends DCLogic {
  state = { items: ["A", "B"], n: 0 };
  renderVals() { return { icon: this.props.icon, items: this.state.items }; }
  componentDidMount() { fetch("https://example.com/beacon").catch(() => {}); new Image().src = "https://example.com/pixel.png"; }
  bump() { this.setState({ n: this.state.n + 1 }); }
  grow() { this.setState({ items: [...this.state.items, "C"] }); }
}</script>`;

  async function openFixture(
    browser: Browser,
    info: TestInfo,
    board: Omit<BoardScene, "file"> = {},
    fonts = usualFonts,
  ) {
    const directory = info.outputPath("boards");
    mkdirSync(directory, { recursive: true });
    writeFileSync(join(directory, "Fixture.dc.html"), fixture(fonts));
    const opened = await openBoard(browser, { file: "Fixture.dc.html", ...board }, directory, staged);
    await fontsLoaded(opened.page, opened.fonts);
    await settle(opened.page, staged.time);
    return opened;
  }

  test("aborts every request of a board outside board.local and still shoots it", async ({ browser }, info) => {
    const opened = await openFixture(browser, info);
    await expect
      .poll(() => opened.blocked.toSorted())
      .toEqual(["https://example.com/beacon", "https://example.com/pixel.png"]);
    expect((await shot(opened.page)).length).toBeGreaterThan(0);
    await opened.close();
  });

  test("keeps every copy of a loop through a re-render, and redraws a loop whose list changed", async ({
    browser,
  }, info) => {
    for (const [actions, items] of [
      [[["bump"]], ["A", "B"]],
      [[["grow"]], ["A", "B", "C"]],
      [
        [["grow"], ["bump"]],
        ["A", "B", "C"],
      ],
    ] as [[string][], string[]][]) {
      const opened = await openFixture(browser, info, { actions });
      await expect(opened.page.locator("li")).toHaveText(items);
      await opened.close();
    }
  });

  test("fails instead of shooting a font that fell back", async ({ browser, page }, info) => {
    // A family the CSS uses with no source font, declared or not, fails the board; one only declared does not.
    for (const fonts of [
      "p{font-family:'Missing'}",
      "@font-face{font-family:'AX Maple';src:url(/_blob/0)} p{font:32px 'AX Maple'}",
      "p{font-family:'PingFang SC',sans-serif}",
    ]) {
      await expect(openFixture(browser, info, {}, fonts)).rejects.toThrow("which has no source font");
    }
    // Spaced CSS, an undeclared family with a source, a system fallback behind it, an unused declaration: each family
    // with a source is served and must load.
    const fonts =
      "@font-face {font-family: 'Axiom Sans'; font-weight: 400; src: url(/_blob/0)} @font-face{font-family:'AX Smiley';src:url(/_blob/1)}" +
      " p{font:32px 'Axiom Sans','PingFang SC'} li{font-family:\"Axiom Mono\"}";
    const opened = await openFixture(browser, info, {}, fonts);
    expect(opened.fonts.map((face) => face.family).toSorted()).toEqual(["Axiom Mono", "Axiom Sans"]);
    await opened.close();
    // A face that fails to load, or a family with no face, is caught where document.fonts.ready would pass.
    await page.setContent(
      "<style>@font-face{font-family:'Broken';src:url(https://example.invalid/broken.woff2)}</style><p>Axiom</p>",
    );
    for (const family of ["Broken", "Undeclared"]) {
      await expect(fontsLoaded(page, [{ family, style: "normal", weight: "400", sample: "Axiom" }])).rejects.toThrow(
        "fonts did not load",
      );
    }
  });

  test("excuses a difference only inside a ruled region, however small", async ({ browser }, info) => {
    const board = await openFixture(browser, info, { props: { icon: "a" } });
    const page = await openFixture(browser, info, { props: { icon: "b" } });
    const icon = { selector: ".icon" };
    const ruled = await compare(
      board.page,
      page.page,
      [{ scene: "", basis: "test", board: icon, page: icon }],
      "tool-ruled",
      info,
    );
    expect(ruled.error).toBeUndefined();
    // A missing 40px icon is 0.12% of the shot, under the 0.2% a page-wide allowance would have excused.
    const unruled = await compare(board.page, page.page, [], "tool-unruled", info);
    expect(unruled.error).toContain("pixels");
    expect(unruled.differentPixels).toBe(40 * 40);
    await board.close();
    await page.close();
  });
});
