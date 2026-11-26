// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import { act } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { ChangeDialog } from "./change";
import zh from "./locales/zh";
import { renderInDom, typeInto } from "./test-dom";
import { Toasts } from "./toasts";

vi.mock("@axiom/app", async (original) => ({
  ...(await original<typeof import("@axiom/app")>()),
  useSession: () => ({ user: { id: "operator" }, account: "site" }),
  useErrors: () => ({
    message: () => "The change was refused",
    field: (error: unknown, name: string) =>
      error instanceof ApiError && error.fields.some((field) => field.pointer === name)
        ? "Check the reason"
        : undefined,
  }),
}));

const parts = () => {
  const dialog = document.querySelector("dialog") as HTMLDialogElement;
  return {
    dialog,
    reason: dialog.querySelector("textarea") as HTMLTextAreaElement,
    submit: dialog.querySelector("button[type=submit]") as HTMLButtonElement,
  };
};

const mounted: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const unmount of mounted.splice(0)) await unmount();
});

it("keeps the entered reason after the server rejects it and submits the corrected value", async () => {
  const refusal = new ApiError(422, null, {
    body: { error: { code: "invalid_request", fields: [{ pointer: "reason", code: "invalid" }] } },
  });
  const send = vi.fn().mockRejectedValueOnce(refusal).mockResolvedValueOnce({ data: "Changed", replayed: false });
  const { unmount } = await renderInDom(
    <ChangeDialog
      title="Edit"
      operation="change-test"
      action="Save"
      read={() => null}
      send={send}
      done={(result: string) => result}
      onClose={vi.fn()}
    />,
  );
  mounted.push(unmount);
  const { reason, submit } = parts();
  await typeInto(reason, "Original");
  await act(async () => submit.click());
  await vi.waitFor(() => expect(reason.getAttribute("aria-invalid")).toBe("true"));
  expect(reason.value).toBe("Original");
  const described = (reason.getAttribute("aria-describedby") ?? "").split(" ");
  expect(described.map((id) => document.getElementById(id)?.textContent)).toContain("Check the reason");
  await typeInto(reason, "Corrected");
  await act(async () => submit.click());
  expect(send.mock.calls.map(([, request]) => request.reason)).toEqual(["Original", "Corrected"]);
});

it("shows the change before and after with its risk, and sends a picked reason", async () => {
  const send = vi.fn().mockResolvedValue({ data: "Changed", replayed: false });
  const { unmount } = await renderInDom(
    <ChangeDialog
      title="Edit"
      operation="change-diff"
      action="Save"
      diff={[{ label: "Who can sign up", before: "Invite code only", after: "Open" }]}
      warn="Open registration lets anyone in."
      read={() => null}
      send={send}
      done={(result: string) => result}
      onClose={vi.fn()}
    />,
  );
  mounted.push(unmount);
  const { dialog, reason, submit } = parts();
  expect(dialog.querySelector("del")?.textContent).toBe("Invite code only");
  expect(dialog.querySelector("ins")?.textContent).toBe("Open");
  expect(dialog.textContent).toContain("Open registration lets anyone in.");
  await act(async () => submit.click());
  expect(send).not.toHaveBeenCalled();
  const pick = [...dialog.querySelectorAll("button")].find((button) => button.textContent === "Incident response");
  await act(async () => pick?.click());
  expect(reason.value).toBe("Incident response");
  await act(async () => submit.click());
  expect(send.mock.calls[0]?.[1].reason).toBe("Incident response");
});

// suspend makes a change with a toast and an undo, and presses Undo in the toast that follows.
async function suspend(undo: (out: unknown) => Promise<unknown>) {
  const onClose = vi.fn();
  const send = vi.fn().mockResolvedValue({ data: { suspended: true }, replayed: false });
  const { unmount } = await renderInDom(
    <>
      <ChangeDialog
        title="Suspend"
        operation={`change-undo-${Math.random()}`}
        action="Suspend"
        read={() => null}
        send={send}
        done={() => "Suspended"}
        toast={() => "Calls suspended"}
        undo={undo}
        onClose={onClose}
      />
      <Toasts />
    </>,
  );
  mounted.push(unmount);
  const { reason, submit } = parts();
  await typeInto(reason, "Abuse report");
  await act(async () => submit.click());
  await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
  const status = document.querySelector("[role=status]") as HTMLElement;
  expect(status.textContent).toContain("Calls suspended");
  const press = [...status.querySelectorAll("button")].find((button) => button.textContent === "Undo");
  await act(async () => press?.click());
  return send;
}

it("says a change in a toast and closes; undo sends the inverse at once with the reason marked as its own", async () => {
  const undo = vi.fn().mockResolvedValue(undefined);
  const send = await suspend(undo);
  expect(undo).toHaveBeenCalledTimes(1);
  const [out, context] = undo.mock.calls[0] ?? [];
  expect(out).toEqual({ suspended: true });
  expect(context.reason).toBe("Undo: Abuse report");
  // the undo is a change of its own, under its own key
  expect(context.key).not.toBe(send.mock.calls[0]?.[1].key);
  expect(zh.change.undoReason).toBe("撤销：{{reason}}");
  await vi.waitFor(() => expect(document.querySelector("[role=status]")?.textContent).toContain("Undone"));
});

it("says an undo whose answer was lost may have been done, and sends it once", async () => {
  const undo = vi.fn().mockRejectedValue(new ApiError(503, null, {}));
  await suspend(undo);
  await vi.waitFor(() =>
    expect(document.querySelector("[role=alert]")?.textContent).toContain("It isn’t known whether the undo"),
  );
  expect(undo).toHaveBeenCalledTimes(1);
});
