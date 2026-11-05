// @vitest-environment jsdom
import { act, useState } from "react";
import { beforeAll, expect, it, vi } from "vitest";
import en from "./locales/en";
import { SuspensionCard } from "./routing-suspension";
import { renderInDom } from "./test-dom";

const sent = vi.hoisted(() => [] as unknown[]);

vi.mock("@axiom/app", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return {
    ...actual,
    can: () => true,
    useErrors: () => ({ message: () => "failed", field: () => undefined }),
    useSession: () => ({ user: { id: "u1" }, account: "a1" }),
  };
});

vi.mock("@axiom/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  return {
    ...actual,
    adminApi: {
      PUT: vi.fn((_path: string, { body }: { body: unknown }) => {
        sent.push(body);
        return Promise.resolve({ data: { suspended: true, since: "2026-11-05T00:00:00Z" }, response: new Response() });
      }),
    },
  };
});

beforeAll(() => {
  // jsdom has the dialog element but not its modal methods.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

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
  expect(container.textContent).toContain(en.routingPresets.suspension.suspended);
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
