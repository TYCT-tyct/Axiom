import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import { AppShell } from "./app-shell";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "common", resources: { en: { common: {} } } });
});

// The print rules are the behavior: with the sidebar off paper the frame must fall back to one column, or the page
// lands in the sidebar's 216px, and the app's notices, reload button included, are no part of a printed receipt
// (docs/frontend.md「打印」).
it("prints the page alone, in one column", () => {
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <AppShell sidebar={<span>{"links"}</span>} banner={<span>{"new version"}</span>}>
        <p>{"page"}</p>
      </AppShell>
    </I18nextProvider>,
  );
  expect(html).toContain("print:block");
  expect(html).toMatch(/class="[^"]*print:hidden[^"]*"><span>new version<\/span>/);
  // The column takes the grid row's height; only the links inside it are viewport-tall and stay in view.
  expect(html).toMatch(/<nav[^>]*class="[^"]*bg-s1[^"]*"[^>]*><div class="sticky top-0 flex h-dvh/);
});
