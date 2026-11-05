// @vitest-environment jsdom
import { act } from "react";
import { beforeAll, expect, it, vi } from "vitest";
import en from "../locales/en";
import { i18n, renderInDom } from "../test-dom";
import { Classes } from "./source-classes";

vi.mock("@axiom/app", async (importOriginal) => (await import("../test-dom")).signedIn(importOriginal));

beforeAll(async () => {
  // jsdom has the dialog element but not its modal methods.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

// A class may be called "new": its edit button opens its editor, not the one that creates a class.
it("edits a class whose slug is new", async () => {
  const cls = {
    slug: "new",
    name: "New supply",
    name_zh: "",
    description: "",
    position: 500,
    fallback: false,
    system: false,
    revision: 3,
    updated_at: "2026-11-03T00:00:00Z",
    rules: { match: "all" as const, conditions: [] },
  };
  const { container, unmount } = await renderInDom(<Classes classes={[cls]} />);
  const edit = [...container.querySelectorAll("li button")].find((b) => b.textContent === en.sourceClasses.edit);
  await act(async () => {
    (edit as HTMLButtonElement).click();
  });
  expect(document.body.textContent).toContain(i18n.t("sourceClasses.editTitle", { name: "New supply" }));
  expect(document.querySelector('input[name="slug"]')).toBeNull();
  await unmount();
});
