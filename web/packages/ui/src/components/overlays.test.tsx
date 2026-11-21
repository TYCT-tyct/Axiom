// @vitest-environment jsdom
import { createInstance } from "i18next";
import { act, type ReactElement } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import zh from "../locales/zh";
import { SidePanel } from "./dialog";
import { Toast } from "./toast";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const i18n = createInstance();
let unmount: (() => void) | undefined;

beforeAll(async () => {
  await i18n.init({ lng: "zh", defaultNS: "common", resources: { zh: { common: zh } } });
  // jsdom leaves HTMLDialogElement.showModal/close unimplemented.
  Object.assign(HTMLDialogElement.prototype, {
    showModal(this: HTMLDialogElement) {
      this.setAttribute("open", "");
    },
    close(this: HTMLDialogElement) {
      this.removeAttribute("open");
      this.dispatchEvent(new Event("close"));
    },
  });
});

afterEach(() => {
  unmount?.();
  unmount = undefined;
  vi.useRealTimers();
});

function mount(element: ReactElement) {
  const container = document.body.appendChild(document.createElement("div"));
  const mounted = createRoot(container);
  act(() => mounted.render(<I18nextProvider i18n={i18n}>{element}</I18nextProvider>));
  unmount = () => {
    act(() => mounted.unmount());
    container.remove();
  };
  return container;
}

it("Toast announces status, auto-dismisses, and supports undo with hover pause", () => {
  vi.useFakeTimers();
  const onDismiss = vi.fn();
  const box = mount(<Toast message="已保存" onDismiss={onDismiss} />);
  const status = box.querySelector('[role="status"]') as HTMLElement;
  expect(status.className).toContain("fxt");
  expect(status.textContent).toContain("已保存");
  act(() => vi.advanceTimersByTime(2600));
  expect(status.className).toContain("out");
  act(() => vi.advanceTimersByTime(260));
  expect(onDismiss).toHaveBeenCalledTimes(1);

  unmount?.();
  const onUndo = vi.fn();
  const onDismissFail = vi.fn();
  const box2 = mount(
    <Toast message="已删除" kind="bad" action={{ label: "撤销", onClick: onUndo }} onDismiss={onDismissFail} />,
  );
  const alert = box2.querySelector('[role="alert"]') as HTMLElement;
  act(() => {
    alert.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
    vi.advanceTimersByTime(6000);
  });
  expect(onDismissFail).not.toHaveBeenCalled();
  act(() => (alert.querySelector("button") as HTMLButtonElement).click());
  expect(onUndo).toHaveBeenCalledTimes(1);
  act(() => vi.advanceTimersByTime(260));
  expect(onDismissFail).toHaveBeenCalledTimes(1);
});

it("SidePanel renders .nkdlg.side with live-save status and closes cleanly", () => {
  const onClose = vi.fn();
  const onRetry = vi.fn();
  const box = mount(
    <SidePanel
      title="密钥详情"
      subtitle="writer-prod"
      saveState="error"
      onRetrySave={onRetry}
      footerLeft={<button type="button">{"删除"}</button>}
      onClose={onClose}
    >
      <p>{"body"}</p>
    </SidePanel>,
  );
  const dialog = box.querySelector("dialog") as HTMLDialogElement;
  expect(dialog.className).toContain("nkdlg");
  expect(dialog.className).toContain("side");
  expect(dialog.hasAttribute("open")).toBe(true);
  const alert = dialog.querySelector('[role="alert"]') as HTMLElement;
  expect(alert.className).toContain("ppsaved");
  expect(alert.className).toContain("bad");
  act(() => (alert.querySelector("button") as HTMLButtonElement).click());
  expect(onRetry).toHaveBeenCalledTimes(1);
  act(() => (dialog.querySelector('button[aria-label="关闭"]') as HTMLButtonElement).click());
  expect(onClose).toHaveBeenCalledTimes(1);
});
