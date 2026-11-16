import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Field, Select, TextArea } from "./field";

// A failed field keeps what was typed and is read with its error; the error is in words, not only a colour.
it.each([
  { name: "Field", html: renderToStaticMarkup(<Field label="名称" error="已被使用" defaultValue="ci" />) },
  { name: "TextArea", html: renderToStaticMarkup(<TextArea label="名称" error="已被使用" defaultValue="ci" />) },
  {
    name: "Select",
    html: renderToStaticMarkup(
      <Select label="名称" error="已被使用" defaultValue="ci" options={[{ value: "ci", label: "ci" }]} />,
    ),
  },
])("$name keeps the value and ties the error to the control", ({ html }) => {
  const [control, error] = [...html.matchAll(/ id="([^"]+)"/g)].map((m) => m[1]);
  expect(html).toMatch(/value="ci"|>ci<\/textarea>|<option value="ci" selected="">/);
  expect(html).toContain('aria-invalid="true"');
  expect(html).toContain(`for="${control}"`);
  expect(html).toContain(`aria-describedby="${error}"`);
  expect(html).toContain(`id="${error}"`);
  expect(html).toContain(">已被使用<");
});

it("reads the hint and the error together, and marks nothing without an error", () => {
  const html = renderToStaticMarkup(<Field label="名称" hint="只有你能看到" />);
  expect(html).not.toContain("aria-invalid");
  expect(html).toMatch(/aria-describedby="[^" ]+-hint"/);
});
