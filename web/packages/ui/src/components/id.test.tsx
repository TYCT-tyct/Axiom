import { createInstance } from "i18next";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import zh from "../locales/zh";
import { Id } from "./id";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "zh", defaultNS: "common", resources: { zh: { common: zh } } });
});

const render = (node: ReactNode) => renderToStaticMarkup(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>);

// An ID too long for its column keeps the head to truncate and the tail whole, and gives a reader the full value and a
// button that copies it (docs/frontend.md「验收门槛」).
it("keeps both ends of a long id, the full value and a copy button", () => {
  const value = "acct_00000000000000000000000001";
  const html = render(<Id value={value} />);
  expect(html).toContain(`title="${value}"`);
  expect(html).toMatch(
    /<span aria-hidden="true" class="min-w-0 truncate">acct_0+<\/span><span aria-hidden="true" class="shrink-0">00000001<\/span>/,
  );
  expect(html).toContain(`<span class="sr-only">${value}</span>`);
  expect(html).toContain('aria-label="复制 ID"');
});
