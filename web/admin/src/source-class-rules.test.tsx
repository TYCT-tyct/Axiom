import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { beforeAll, expect, it } from "vitest";
import en from "./locales/en";
import { ConditionRows } from "./source-class-rules";

const i18n = createInstance();

beforeAll(async () => {
  await i18n.init({ lng: "en", defaultNS: "admin", resources: { en: { admin: en } } });
});

function render(initial?: Parameters<typeof ConditionRows>[0]["initial"]) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <ConditionRows initial={initial} />
    </I18nextProvider>,
  );
}

it("renders a header rule with its operator and value", () => {
  const html = render({
    match: "all",
    conditions: [{ evidence: "header", name: "X-Pool", operator: "prefix", value: "pool-" }],
  });
  expect(html).toContain('name="count" value="1"');
  expect(html).toContain('name="name-0"');
  expect(html).toContain('value="X-Pool"');
  expect(html).toContain('name="operator-0"');
  expect(html).toContain('name="value-0"');
  expect(html).toContain('value="pool-"');
});

it("says that a class without conditions matches nothing", () => {
  const html = render();
  expect(html).toContain('name="count" value="0"');
  expect(html).toContain("No conditions yet; this class matches no endpoint.");
});
