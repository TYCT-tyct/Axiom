import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import { glyphNames } from "./components/glyph";
import { ComponentDemo, demos } from "./demo";
import zh from "./locales/zh";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "zh", defaultNS: "common", resources: { zh: { common: zh } } });
});

// The demo page shows every primitive of port-design §3.1 that F1b owns, so the screenshot comparison has each of them.
it("renders a demo of every primitive and every icon", () => {
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <ComponentDemo />
    </I18nextProvider>,
  );
  expect(Object.keys(demos)).toEqual(
    expect.arrayContaining(["icon", "page-head", "button", "card", "setting-row", "table", "segmented", "switch"]),
  );
  expect(Object.keys(demos)).toEqual(expect.arrayContaining(["tag", "avatar", "field", "note", "popover"]));
  for (const name of Object.keys(demos)) {
    expect(html).toContain(`data-demo="${name}"`);
  }
  expect(glyphNames).toHaveLength(27);
  for (const name of glyphNames) {
    expect(html).toContain(`_${name}_`);
  }
});
