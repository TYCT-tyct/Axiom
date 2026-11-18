import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { Json } from "./parts";

// A long record scrolls inside its box (max-h-96): axe's scrollable-region-focusable fails a box keyboard users cannot
// focus, so the box is a named region in the tab order.
it("puts a record in a named region keyboard users can focus to scroll", () => {
  const html = renderToStaticMarkup(<Json value={{ route: ["a", "b"] }} label="Route" />);
  expect(html).toMatch(/<pre[^>]* role="region" aria-label="Route" tabindex="0"/);
  expect(html).toContain("&quot;route&quot;");
});
