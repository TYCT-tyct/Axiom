import type { Locator, Page } from "@playwright/test";
import type { Region } from "../../packages/boards/regions.ts";

function captureContext(original: Locator, region: Region) {
  const omit = region.name === "PartDialog" ? [] : (region.omit ?? []);
  return original.evaluate(
    (target, { omit, name }) => {
      for (const selector of omit) for (const element of target.querySelectorAll(selector)) element.remove();
      const controls = Array.from(target.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input, textarea"));
      const index = controls.indexOf(document.activeElement as HTMLInputElement | HTMLTextAreaElement);
      const focused = controls[index];
      const active = focused ? { index, start: focused.selectionStart, end: focused.selectionEnd } : undefined;
      const control = controls[0];
      const field = {
        id: control?.id,
        initial: control?.value,
        placeholder: control?.placeholder,
        autocomplete: control?.autocomplete,
        messageClass: "nkhint",
        inputType: "text",
      };
      target.setAttribute("data-board-replace", "");
      const layout = target.closest(".board")?.cloneNode(true) as HTMLElement;
      target.removeAttribute("data-board-replace");
      // Parsed style attributes are rejected by the production preview's CSP. Carry declarations separately.
      const styled = [layout, ...layout.querySelectorAll<HTMLElement>("[style]")].map((element, index) => {
        element.setAttribute("data-board-style", String(index));
        const declarations = Array.from(element.style, (name) => ({
          name,
          value: element.style.getPropertyValue(name),
          priority: element.style.getPropertyPriority(name),
        }));
        element.removeAttribute("style");
        return declarations;
      });
      const markup = layout.outerHTML;
      const rules = Array.from(document.styleSheets).flatMap((sheet) => Array.from(sheet.cssRules));
      const fonts = rules.filter((rule) => rule instanceof CSSFontFaceRule);
      const css = `${fonts.map((rule) => rule.cssText.replaceAll("https://board.local/fonts/", "/fonts/")).join("\n")}
      @scope (:root) to ([data-board-part]) {
        ${rules
          .filter((rule) => !(rule instanceof CSSFontFaceRule))
          .map((rule) => rule.cssText)
          .join("\n")}
      }`;
      const sample =
        name === "AdminCodeBlock"
          ? {
              code: Array.from(target.childNodes, (node) => ({
                token: node.nodeType === Node.ELEMENT_NODE,
                text: node.textContent,
              })),
            }
          : { fill: target.querySelector("i")?.getAttribute("style") };
      return { field, markup, css, active, sample, styled };
    },
    { omit, name: region.name },
  );
}

// The reference supplies the layout outside the cut. Only generated CSS can match inside the replacement.
export async function renderPart(page: Page, original: Locator, region: Region) {
  // The observer on the list footer would otherwise change the sample when the locator scrolls it into view.
  if (region.name === "PartLoadMore") {
    await original.evaluate((footer) => footer.parentElement?.replaceChild(footer.cloneNode(true), footer));
  }
  const { css, ...fixture } = await captureContext(original, region);
  await page.route("**/part-context.css", (route) => route.fulfill({ contentType: "text/css", body: css }));
  type Fixture = typeof fixture;
  await page.waitForFunction(() => "paintPart" in window);
  await page.evaluate(
    (data) => (window as unknown as { paintPart: (fixture: Fixture) => Promise<void> }).paintPart(data),
    fixture,
  );
  await page.evaluate(() => document.fonts.ready.then(() => {}));
  await page.clock.runFor(5000);
}

// Runs in the generated demo's browser entry, where the generated imports and their CSS come from Vite.
export const partProofScript = `
  window.paintPart = async ({ markup, field, active, sample, styled }) => {
    await scene.sheet?.();
    const cut = async (part, extra = {}) => {
      const [Region, demo, zh] = await Promise.all([scene[part](), scene[part + "Data"](), scene[part + "ZH"]()]);
      const props = demo(key => zh[key]);
      return createElement(Region, { ...props, ...extra, values: { ...props.values, ...extra.values } });
    };
    const extras = {
      AdminCodeBlock: async () => ({
        children: await Promise.all(sample.code.map(({token, text}, key) =>
          token ? cut("AdminCodeToken", { key, text: { token: text } }) : text)),
      }),
      AdminCopyButton: async () => ({ values: { tone: "pri" } }),
      AdminCopiedButton: async () => ({ values: { tone: "sec" } }),
      AdminElapsedTrack: async () => ({ values: { fill: sample.fill } }),
      PartDialog: async () => ({ children: await cut("PartDialogSample"), values: { primaryClass: "pri", placement: "border: 0; margin: 0; padding: 0; color: inherit", dialog: element => { if (element) element.open = true; } } }),
      PartTextField: async () => ({ values: field }),
      PartTextAreaField: async () => ({ values: field }),
      PartFilterBar: async () => ({ children: await cut("PartFilterSample") }),
      PartSortableHeader: async () => ({ values: { selected: "on" } }),
      PartLoading: async () => ({ children: await cut("PartLoadingSample") }),
    };
    const part = await cut(name, await extras[name]?.());
    await new Promise((resolve, reject) => {
      const context = document.createElement("link");
      context.rel = "stylesheet";
      context.href = "/part-context.css";
      context.onload = resolve;
      context.onerror = reject;
      document.head.append(context);
    });
    const staging = document.createElement("div");
    flushSync(() => createRoot(staging).render(part));
    const elements = Array.from(staging.children);
    const target = name === "PartDialog" ? elements[0].querySelector(".nkdlg") : elements[0];
    elements.forEach(el => el.setAttribute("data-board-part", ""));
    target.setAttribute("data-board-part", "target");
    document.getElementById("root").innerHTML = markup;
    for (const element of document.querySelectorAll("[data-board-style]")) {
      for (const {name, value, priority} of styled[Number(element.dataset.boardStyle)]) {
        element.style.setProperty(name, value, priority);
      }
      element.removeAttribute("data-board-style");
    }
    document.querySelector("[data-board-replace]").replaceWith(...elements);
    if (name === "PartDialog") document.querySelector(".fr > .nkscrim")?.remove();
    if (active) {
      const control = target.querySelectorAll("input, textarea")[active.index];
      control?.focus({ preventScroll: true });
      if (active.start !== null) control?.setSelectionRange(active.start, active.end);
    }
  };
`;
