// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { dropMailLink, keepMailLink, type MailRoute, useMailLink } from "./mail-link";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The token each route's page reads, after each change.
const held: Partial<Record<MailRoute, string>> = {};
function Probe({ route }: { route: MailRoute }) {
  held[route] = useMailLink(route).token;
  return null;
}

it("replaces the token on each arrival, and leaves another route's token to it", async () => {
  const root = createRoot(document.createElement("div"));
  await act(async () =>
    root.render(
      <>
        <Probe route="/verify-email" />
        <Probe route="/reset-password" />
      </>,
    ),
  );
  await act(async () => keepMailLink("/verify-email", "token=first"));
  expect(held).toEqual({ "/verify-email": "first", "/reset-password": "" });
  // an arrival whose fragment holds no token replaces the one held
  await act(async () => keepMailLink("/verify-email", "token="));
  expect(held["/verify-email"]).toBe("");
  // a page that takes over is not cleared by the one it replaced leaving after it
  await act(async () => keepMailLink("/reset-password", "token=second"));
  await act(async () => dropMailLink("/verify-email"));
  expect(held).toEqual({ "/verify-email": "", "/reset-password": "second" });
  await act(async () => dropMailLink("/reset-password"));
  expect(held["/reset-password"]).toBe("");
  act(() => root.unmount());
});
