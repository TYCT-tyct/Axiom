// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import { act } from "react";
import { expect, it, vi } from "vitest";
import { ChangeDialog } from "./change";
import { renderInDom } from "./test-dom";

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
  try {
    const dialog = document.querySelector("dialog") as HTMLDialogElement;
    const reason = dialog.querySelector<HTMLTextAreaElement>("textarea") as HTMLTextAreaElement;
    const submit = dialog.querySelector<HTMLButtonElement>("button[type=submit]") as HTMLButtonElement;
    reason.value = "Original";
    await act(async () => submit.click());
    await vi.waitFor(() => expect(reason.getAttribute("aria-invalid")).toBe("true"));
    expect(reason.value).toBe("Original");
    expect(document.getElementById(reason.getAttribute("aria-describedby") ?? "")?.textContent).toBe(
      "Check the reason",
    );
    reason.value = "Corrected";
    await act(async () => submit.click());
    expect(send.mock.calls.map(([, request]) => request.reason)).toEqual(["Original", "Corrected"]);
  } finally {
    await unmount();
  }
});
