// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { focusPage } from "./app-shell";

afterEach(() => {
  document.body.replaceChildren();
});

const open = (drawer: boolean) => {
  const dialog = document.body.appendChild(document.createElement("dialog"));
  dialog.setAttribute("open", "");
  if (drawer) dialog.dataset.drawer = "";
  dialog.close = () => dialog.removeAttribute("open");
  return dialog;
};

// A dialog still open as a page change resolves would make the page inert, so it is closed; a drawer is the arriving
// page's own, kept in its address, and stays. The first load changes no page and closes nothing.
it("closes the dialogs open as a navigation resolves, but not a drawer, and nothing on the first load", () => {
  const dialog = open(false);
  const drawer = open(true);
  focusPage({ pathChanged: true });
  expect(dialog.hasAttribute("open")).toBe(true);
  focusPage({ fromLocation: {}, pathChanged: true });
  expect(dialog.hasAttribute("open")).toBe(false);
  expect(drawer.hasAttribute("open")).toBe(true);
});
