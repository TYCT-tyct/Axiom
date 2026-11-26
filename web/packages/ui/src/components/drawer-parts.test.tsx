import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { DrawerSection, FactList } from "./drawer-parts";

it("names a section by its title and keeps what sits at its end", () => {
  const html = renderToStaticMarkup(
    <DrawerSection title="Money" aside={<b>{"tag"}</b>}>
      <p>{"body"}</p>
    </DrawerSection>,
  );
  expect(html).toMatch(/<h3>Money<\/h3><b>tag<\/b>/);
  expect(html).toContain("<p>body</p>");
});

it("lists each fact as a term and its value, with no box of its own", () => {
  const html = renderToStaticMarkup(
    <FactList
      rows={[
        ["Charged", "¥1.20"],
        ["Paid", <i key="paid">{"¥0.80"}</i>],
      ]}
    />,
  );
  expect(html.match(/<dt>/g)).toHaveLength(2);
  expect(html).toContain("<dd><i>¥0.80</i></dd>");
  expect(html).not.toContain("<section");
});
