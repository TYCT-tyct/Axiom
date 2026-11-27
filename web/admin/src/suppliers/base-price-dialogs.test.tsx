// @vitest-environment jsdom
import { act } from "react";
import { afterEach, expect, it } from "vitest";
import { renderInDom, typeInto } from "../test-dom";
import { readBase } from "./base-price";
import { BaseFields } from "./base-price-dialogs";

const invalid = { currency: "currency", rate: "rate", none: "none", evidence: "evidence" };
const official = { v: 1 as const, currency: "USD", rates: { input: "0.000003" } };

const mounted: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const unmount of mounted.splice(0)) await unmount();
});

// What the form sends is what it shows, however often the person switches between the official price and typed ones:
// the typed prices are kept while hidden, and the source field alone says which is sent.
it("sends exactly what is shown after switching back and forth", async () => {
  const { container, unmount } = await renderInDom(
    <form>
      <BaseFields reference={{ schedule: official, source: "manual" }} currency="USD" />
    </form>,
  );
  mounted.push(unmount);
  const form = container.querySelector("form") as HTMLFormElement;
  const choose = (value: "official" | "entered") =>
    act(async () => (form.querySelector(`input[type=radio][value=${value}]`) as HTMLInputElement).click());
  const input = () => form.querySelector("input[name=rate_input]") as HTMLInputElement;
  const sent = () =>
    readBase(Object.fromEntries(new FormData(form) as unknown as Iterable<[string, string]>), "USD", invalid);
  const typedShown = () => !input().closest("[hidden]");

  expect(sent()).toEqual({ kind: "official" });
  await choose("entered");
  expect(input().value).toBe("3");
  await typeInto(input(), "9");
  await choose("official");
  expect(typedShown()).toBe(false);
  expect(sent()).toEqual({ kind: "official" });
  await choose("entered");
  expect(typedShown()).toBe(true);
  expect(input().value).toBe("9");
  expect(sent()).toEqual({ kind: "entered", price: { v: 1, currency: "USD", rates: { input: "0.000009" } } });
});
