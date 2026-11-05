// @vitest-environment jsdom
import { act, useState } from "react";
import { expect, it, vi } from "vitest";
import en from "./locales/en";
import { SuspensionCard, usePresetsHold } from "./routing-suspension";
import { renderInDom } from "./test-dom";

const sent = vi.hoisted(() => [] as unknown[]);

vi.mock("@axiom/app", async (importOriginal) =>
  (await import("./test-dom")).signedIn(importOriginal, ["sites.manage"]),
);

vi.mock("@axiom/api", async (importOriginal) =>
  (await import("./test-dom")).withAdminApi(importOriginal, {
    GET: vi.fn(() =>
      Promise.resolve({ data: { suspended: true, since: "2026-11-05T00:00:00Z" }, response: new Response() }),
    ),
    PUT: vi.fn((_path: string, { body }: { body: unknown }) => {
      sent.push(body);
      return Promise.resolve({ data: { suspended: true, since: "2026-11-05T00:00:00Z" }, response: new Response() });
    }),
  }),
);

type Suspension = { suspended: boolean; since?: string };
let refetched: (s: Suspension) => void = () => {};

function Card() {
  const [suspension, setSuspension] = useState<Suspension>({ suspended: false });
  refetched = setSuspension;
  return <SuspensionCard suspension={suspension} />;
}

const button = (text: string) =>
  [...document.querySelectorAll("button")].find((b) => b.textContent === text) as HTMLButtonElement | undefined;

// Another administrator suspends presets while this one writes the reason for suspending them: the refetch turns the
// card to suspended, but the open dialog still suspends, and sends that.
it("keeps the open dialog's target when a refetch shows the other state", async () => {
  const { container, unmount } = await renderInDom(<Card />);
  await act(async () => button(en.routingPresets.suspension.suspend)?.click());
  await act(async () => refetched({ suspended: true, since: "2026-11-05T00:00:00Z" }));
  expect(container.textContent).toContain("Suspended since");
  expect(document.body.textContent).toContain(en.routingPresets.suspension.suspendTitle);
  expect(document.body.textContent).not.toContain(en.routingPresets.suspension.resumeTitle);
  const reason = document.querySelector<HTMLTextAreaElement>('textarea[name="reason"]');
  if (!reason) {
    throw new Error("no reason field");
  }
  reason.value = "incident";
  const submit = [...document.querySelectorAll<HTMLButtonElement>("dialog button[type=submit]")].at(-1);
  await act(async () => submit?.click());
  expect(sent).toEqual([{ suspended: true, reason: "incident" }]);
  await unmount();
});

// A control that changes presets must not look usable while the node would refuse the change.
it("disables a preset-changing control with the reason while presets are suspended", async () => {
  function Control() {
    return <button type="button" {...usePresetsHold()} />;
  }
  const { container, unmount } = await renderInDom(<Control />);
  const control = container.querySelector("button");
  await vi.waitFor(() => expect(control?.disabled).toBe(true));
  expect(control?.title).toBe(en.routingPresets.suspension.held);
  await unmount();
});
