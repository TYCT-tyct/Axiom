// @vitest-environment jsdom
import { act, type ReactNode, useState } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { useKeptPage } from "./board-shell";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// A page with something only it holds, as the key page holds a new key's secret.
function Page() {
  const [secret] = useState(() => Math.random().toString(36));
  return <p>{secret}</p>;
}

const Wide = ({ children }: { children: ReactNode }) => <main className="wide">{children}</main>;
const Phone = ({ children }: { children: ReactNode }) => <section className="phone">{children}</section>;

function Shell({ phone }: { phone: boolean }) {
  const { page, slot } = useKeptPage(<Page />);
  return (
    <>
      {phone ? <Phone>{slot}</Phone> : <Wide>{slot}</Wide>}
      {page}
    </>
  );
}

it("keeps the page and what it holds while the shell changes boards", () => {
  const container = document.createElement("div");
  const root = createRoot(container);
  act(() => root.render(<Shell phone />));
  const secret = container.querySelector(".phone p")?.textContent;
  expect(secret).toBeTruthy();
  act(() => root.render(<Shell phone={false} />));
  expect(container.querySelector(".phone")).toBeNull();
  expect(container.querySelector(".wide p")?.textContent).toBe(secret);
  act(() => root.unmount());
});
