import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { Browser, Page } from "@playwright/test";
import { boardProps, readBoard } from "../../packages/boards/source.ts";
import { boardFonts, type FontFace, fontRoute, usedFaces } from "./fonts.ts";
import { type Stage, stage, stagePaused } from "./stage.ts";

// The reference half of a comparison (docs/frontend.md「截图对比」): the board's own Component class, run in a browser context
// of its own that is offline, keeps no cookies or storage and is thrown away after the shot. Every request outside
// board.local is aborted, so a board script that reaches for the network cannot change the reference. support.js is
// replaced by the harness below, as in the design tool's harnesses. Engines stay off for product comparisons; the
// mechanical conversion proofs explicitly name their pinned engines and mount under a paused clock.

const origin = "https://board.local";

// A board and how to show it: its file in AXIOM_DESIGN_DIR, the props the design tool's editor would set (theme, view,
// role...), and the Component's own methods to call after it mounts, each as [name, ...arguments] (["go", "keys"]).
export type BoardScene = {
  file: string;
  props?: Record<string, string>;
  actions?: ([string, ...unknown[]] | { click: string })[];
  engines?: string[];
  preloadFonts?: boolean;
};

export type OpenBoard = {
  page: Page;
  // The faces the board draws text in, each to be loaded before the shot (fontsLoaded).
  fonts: FontFace[];
  // The requests the board made outside board.local, each aborted.
  blocked: string[];
  close: () => Promise<void>;
};

// The parts of a .dc.html board: the <helmet> (styles), the template between </helmet> and </x-dc>, the Component
// script and the defaults of its props.
function parts(html: string) {
  const helmet = html.match(/<helmet>([\s\S]*?)<\/helmet>/)?.[1];
  const template = html.match(/<\/helmet>([\s\S]*)<\/x-dc>/)?.[1];
  const script = html.match(/data-dc-script[^>]*>([\s\S]*?)<\/script>/)?.[1];
  if (helmet === undefined || template === undefined || script === undefined) {
    throw new Error("not a board: no <helmet>, template or data-dc-script");
  }
  const defaults: Record<string, unknown> = {};
  for (const [key, prop] of Object.entries(boardProps(html))) {
    if (prop && typeof prop === "object" && "default" in prop) {
      defaults[key] = prop.default;
    }
  }
  return { helmet, template, script, defaults };
}

// mark numbers every element whose attributes or text hold a template hole, so a re-render patches the live element
// instead of replacing the tree (which would drop the refs the Component holds). ref and on* holes are bound once.
function mark(template: string) {
  let n = 0;
  return template.replace(/<([a-zA-Z][\w-]*)((?:[^>"]|"[^"]*")*)>([^<]*)/g, (whole, name, attributes, text) => {
    const hole = attributes.replace(/(ref|on[A-Z]\w*)="[^"]*"/g, "").includes("{{") || text.includes("{{");
    if (!hole || attributes.trimEnd().endsWith("/")) {
      return whole;
    }
    n += 1;
    const classes = attributes.match(/\bclass="([^"]*\{\{[^"]*)"/)?.[1];
    const authored = classes ? ` data-design-class="${encodeURIComponent(classes)}"` : "";
    return `<${name}${attributes}${authored} data-design-key="${n}">${text}`;
  });
}

// Inside a <script>, "</script>" in a string would end the element; JSON escapes "<" so nothing can.
const inline = (value: unknown) => JSON.stringify(value).replaceAll("<", "\\u003c");

// The support.js stand-in: the base class every board's Component extends.
const dcLogic = `class DCLogic {
  constructor() { this.props = window.__props; this.state = {}; }
  setState(s) { Object.assign(this.state, typeof s === "function" ? s(this.state) : s); if (window.__patch) window.__patch(); }
}`;

// The board script's class: a global binding of the board's page, not a property of window.
declare const Component: new () => Record<string, unknown>;

// The functions below run in the board's page, injected by their source in a scope of their own (boardDocument), so
// they cannot collide with the board script's globals.
// Ported from handoff/src/mini.js and the harnesses: {{ holes }} filled from the Component's renderVals, sc-for loops
// expanded, ref holes collected into refs to be bound after the HTML is in place.
function look(key: string, scope: unknown) {
  return key.split(".").reduce<unknown>((o, k) => (o == null ? o : (o as Record<string, unknown>)[k]), scope);
}

function fill(text: string, scope: unknown, refs: unknown[]) {
  return text
    .replace(/(ref|on[A-Z]\w*)="\{\{\s*([\w.]+)\s*\}\}"/g, (_, attribute: string, key) => {
      refs.push(look(key, scope));
      const name = attribute === "ref" ? "ref" : `event-${attribute.slice(2).toLowerCase()}`;
      return `data-${name}="${refs.length - 1}"`;
    })
    .replace(/\{\{\s*([\w.$]+)\s*\}\}/g, (_, key) => {
      const value = look(key, scope);
      return typeof value === "function" ? "" : String(value ?? "");
    });
}

export function render(text: string, scope: Record<string, unknown>, refs: unknown[]): string {
  const start = text.indexOf("<sc-for");
  if (start < 0) return fill(text, scope, refs);
  const tagEnd = text.indexOf(">", start);
  const tag = text.slice(start, tagEnd + 1);
  let depth = 1;
  let at = tagEnd + 1;
  while (depth) {
    const open = text.indexOf("<sc-for", at);
    const close = text.indexOf("</sc-for>", at);
    [depth, at] = open >= 0 && open < close ? [depth + 1, open + 7] : [depth - 1, close + 9];
  }
  const inner = text.slice(tagEnd + 1, at - 9);
  const list = (look(tag.match(/list="\{\{\s*([\w.]+)\s*\}\}"/)?.[1] ?? "", scope) ?? []) as unknown[];
  const as = tag.match(/as="(\w+)"/)?.[1] ?? "item";
  // Each copy of the loop's body gets marks of its own (k.index, nested k.inner.outer), so patch pairs every copy with
  // its live element instead of all of them with the first.
  const items = list
    .map((item, index) =>
      render(inner, { ...scope, [as]: item, $index: index }, refs).replace(
        / data-design-key="([\d.]+)"/g,
        (_, k) => ` data-design-key="${k}.${index}"`,
      ),
    )
    .join("");
  return fill(text.slice(0, start), scope, refs) + items + render(text.slice(at), scope, refs);
}

// patch carries what a re-render changed onto the live elements the marks pair up, and says false without changing
// anything when the marked elements differ, as when a loop's list grew or shrank: the caller then renders anew.
function patch(root: HTMLElement, next: HTMLElement) {
  const marks = (tree: HTMLElement) =>
    [...tree.querySelectorAll<HTMLElement>("[data-design-key]")].map((el) => el.dataset.designKey);
  if (marks(root).join() !== marks(next).join()) return false;
  for (const fresh of next.querySelectorAll<HTMLElement>("[data-design-key]")) {
    const live = root.querySelector(`[data-design-key="${fresh.dataset.designKey}"]`) as Element;
    for (const name of ["class", "style", "aria-hidden", "aria-pressed", "lang", "title"]) {
      const value = fresh.getAttribute(name);
      if (value !== null && live.getAttribute(name) !== value) live.setAttribute(name, value);
    }
    const leaf = fresh.children.length === 0 && live.children.length === 0 && live.tagName !== "CANVAS";
    if (leaf && live.textContent !== fresh.textContent) live.textContent = fresh.textContent;
  }
  return true;
}

// Bind authored event and ref holes after the complete tree exists.
export function bind(root: HTMLElement, refs: unknown[]) {
  for (const element of root.querySelectorAll<HTMLElement>("*")) {
    if (element.hasAttribute("data-ref"))
      (refs[Number(element.dataset.ref)] as ((el: Element) => void) | undefined)?.(element);
    for (const attribute of element.attributes)
      if (attribute.name.startsWith("data-event-"))
        element.addEventListener(attribute.name.slice(11), refs[Number(attribute.value)] as EventListener);
  }
}

// mount renders the board, binds its refs, re-renders on every setState (DCLogic calls __patch), calls
// componentDidMount. Actions run after mounting, so clicks can wait for content drawn by native observers.
function mount(template: string) {
  const c = new Component();
  const values = () => (c.renderVals as () => Record<string, unknown>)();
  const root = document.getElementById("root") as HTMLElement;
  const refs: unknown[] = [];
  root.innerHTML = render(template, values(), refs);
  bind(root, refs);
  (window as unknown as { __patch: () => void }).__patch = () => {
    const next = document.createElement("div");
    const fresh: unknown[] = [];
    next.innerHTML = render(template, values(), fresh);
    if (!patch(root, next)) {
      root.replaceChildren(...next.childNodes);
      bind(root, fresh);
    }
  };
  (c.componentDidMount as (() => void) | undefined)?.call(c);
  Object.assign(window, {
    __boardAction: ([name, ...args]: [string, ...unknown[]]) => (c[name] as (...a: unknown[]) => void).apply(c, args),
  });
}

// boardDocument builds the page served at board.local: the board's helmet with its fonts replaced by the frozen source fonts,
// then the support.js stand-in, the board's script and the mount call.
function boardDocument(board: BoardScene, directory: string) {
  const { helmet, template, script, defaults } = parts(readBoard(join(directory, board.file)));
  const fonts = boardFonts(helmet, `${origin}/fonts/`);
  return {
    fonts: fonts.faces,
    html: `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">${fonts.helmet}<style>body{margin:0}</style>
${(board.engines ?? []).map((name) => `<script src="/engines/${name}.js"></script>`).join("\n")}
<script>window.__props = ${inline({ ...defaults, ...board.props })};
${dcLogic}</script></head><body><div id="root"></div>
<script>${script}</script>
<script>(async () => {
${[look, fill, render, patch, bind, mount].join("\n")}
${board.engines && board.preloadFonts !== false ? `await Promise.all(${inline(fonts.faces)}.map(f => document.fonts.load(f.style + ' ' + f.weight.split(' ')[0] + ' 16px "' + f.family + '"')));` : ""}
mount(${inline(mark(template))});
})();</script></body></html>`,
  };
}

// openBoard renders a board in a fresh offline context at the given viewport and colour scheme, staged like the page it
// is compared with (stage.ts), and stops when the board has mounted and its fonts have loaded.
export async function openBoard(
  browser: Browser,
  board: BoardScene,
  directory: string,
  view: Stage,
): Promise<OpenBoard> {
  const { html, fonts } = boardDocument(board, directory);
  const context = await browser.newContext({
    offline: true,
    viewport: view.viewport,
    deviceScaleFactor: 1,
    locale: "zh-CN",
    timezoneId: view.timezoneId,
    colorScheme: view.colorScheme,
  });
  context.setDefaultTimeout(30_000);
  const blocked: string[] = [];
  await context.route("**/*", async (route) => {
    const url = route.request().url();
    if (url === `${origin}/`) {
      return route.fulfill({ contentType: "text/html; charset=utf-8", body: html });
    }
    const engine = url.match(/\/engines\/([\w-]+)\.js$/)?.[1];
    if (engine && board.engines?.includes(engine)) {
      return route.fulfill({
        contentType: "text/javascript",
        body: readFileSync(
          join(directory, existsSync(join(directory, `${engine}.js.txt`)) ? `${engine}.js.txt` : `${engine}.js`),
          "utf8",
        ),
      });
    }
    if (url.startsWith(`${origin}/fonts/`)) {
      return fontRoute(route, url.slice(`${origin}/fonts/`.length));
    }
    blocked.push(url);
    return route.abort("blockedbyclient");
  });
  const page = await context.newPage();
  const errors: Error[] = [];
  page.on("pageerror", (error) => errors.push(error));
  if (board.engines) await stagePaused(page, view);
  else await stage(page, view);
  await page.goto(`${origin}/`);
  if (board.engines) await page.locator("#root > *").first().waitFor({ state: "attached" });
  try {
    if (errors.length)
      throw new Error(`the board ${board.file} failed to render: ${errors.map((e) => e.message).join("; ")}`);
    for (const action of board.actions ?? []) {
      if (Array.isArray(action))
        await page.evaluate((action) => {
          (window as unknown as { __boardAction: (action: [string, ...unknown[]]) => void }).__boardAction(action);
        }, action);
      else await page.locator(action.click).first().dispatchEvent("click");
    }
    if (errors.length)
      throw new Error(`the board ${board.file} failed to render: ${errors.map((e) => e.message).join("; ")}`);
    return { page, fonts: await usedFaces(page, fonts), blocked, close: () => context.close() };
  } catch (error) {
    await context.close();
    throw error;
  }
}
