// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import { createInstance } from "i18next";
import { act, type ReactNode, StrictMode, useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import zh from "../locales/zh";
import { testRoot } from "../test-root";
import { useExplicitSave, useLiveSave } from "../write";
import { CreateDialog } from "./create-dialog";
import { EditPanel } from "./edit-panel";
import { moduleCss, reduced } from "./module-css";
import { DataState } from "./states";
import { ToastProvider, useToast, useUndo } from "./toast";

const i18n = createInstance();
const shown = new Set<HTMLDialogElement>();
const dom = testRoot();

// jsdom has no modal dialogs, animations or media queries. The stubs do what a browser does with them: showModal
// opens the dialog (the browser also makes the page behind it inert, which jsdom cannot show, so the tests check that
// showModal was the way it opened), close closes it and fires close, and nothing animates.
beforeAll(async () => {
  await i18n.init({ lng: "zh", defaultNS: "common", resources: { zh: { common: zh } } });
  HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) {
    this.setAttribute("open", "");
    shown.add(this);
  };
  HTMLDialogElement.prototype.close = function (this: HTMLDialogElement) {
    if (this.open) {
      this.removeAttribute("open");
      this.dispatchEvent(new Event("close"));
    }
  };
  Element.prototype.getAnimations = () => [];
  Element.prototype.animate = vi.fn();
  window.matchMedia = (query: string) => ({ matches: false, media: query }) as MediaQueryList;
});

beforeEach(() => shown.clear());
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

// StrictMode as the apps start (start.tsx): effects run, are cleaned up and run again on mount.
const render = (node: ReactNode) =>
  act(() =>
    dom.root.render(
      <StrictMode>
        <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
      </StrictMode>,
    ),
  );
const dialog = () => document.querySelector("dialog");
const button = (name: string) =>
  [...document.querySelectorAll("button")].find((b) => b.textContent === name) as HTMLButtonElement;
const field = () => document.querySelector("dialog input") as HTMLInputElement;
const settle = () => act(async () => {});
// what a browser does on Esc: a cancelable cancel event on the dialog
const pressEscape = async () => {
  act(() => {
    dialog()?.dispatchEvent(new Event("cancel", { cancelable: true }));
  });
  await settle();
};
// a press on the backdrop lands on the dialog element itself
const pressBackdrop = async () => {
  act(() => dialog()?.click());
  await settle();
};
// type sets an input's value the way React hears it.
function type(input: HTMLInputElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

// A fake API: every call is recorded with its body and stays out until the test answers it.
function fakeApi<T, R = T>() {
  const calls: { body: T; resolve: (value: R) => void; reject: (error: unknown) => void }[] = [];
  const send = (body: T) => new Promise<R>((resolve, reject) => calls.push({ body, resolve, reject }));
  return { calls, send };
}

function Opener({ overlay }: { overlay: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        {"open"}
      </button>
      {open ? overlay(() => setOpen(false)) : null}
    </>
  );
}

const open = () => {
  const trigger = button("open");
  trigger.focus();
  act(() => trigger.click());
  return trigger;
};

const closed = vi.fn();
beforeEach(() => closed.mockClear());

function newKey(create: (draft: { name: string }) => Promise<string>) {
  render(
    <Opener
      overlay={(close) => (
        <CreateDialog
          title="新建 key"
          initial={{ name: "" }}
          create={create}
          result={(secret) => ({ title: "已创建", body: <code>{secret}</code> })}
          onClose={(created) => {
            closed(created);
            close();
          }}
        >
          {(draft, set) => (
            <input aria-label="名称" value={draft.name} onChange={(e) => set({ name: e.target.value })} />
          )}
        </CreateDialog>
      )}
    />,
  );
  return open();
}

it("takes a keyboard user into a named modal dialog and back to the trigger when it closes", async () => {
  const trigger = newKey(fakeApi<{ name: string }, string>().send);
  const opened = dialog() as HTMLDialogElement;
  expect(shown.has(opened)).toBe(true);
  expect(document.activeElement).toBe(field());
  const title = document.getElementById(opened.getAttribute("aria-labelledby") ?? "");
  expect(title?.textContent).toBe("新建 key");

  await pressEscape();
  expect(dialog()).toBeNull();
  expect(closed).toHaveBeenCalledWith(undefined);
  expect(document.activeElement).toBe(trigger);
});

it("keeps a dialog with something typed open on Esc and the backdrop; Cancel still closes it", async () => {
  newKey(fakeApi<{ name: string }, string>().send);
  type(field(), "ci");
  await pressEscape();
  await pressBackdrop();
  expect(dialog()?.open).toBe(true);
  act(() => button("取消").click());
  await settle();
  expect(dialog()).toBeNull();
});

it("shows the result once, which only Done ends", async () => {
  const api = fakeApi<{ name: string }, string>();
  newKey(api.send);
  type(field(), "ci");
  act(() => button("创建").click());
  expect(api.calls.map((c) => c.body)).toEqual([{ name: "ci" }]);
  await act(async () => api.calls[0]?.resolve("sk-ax-v1-secret"));
  expect(dialog()?.textContent).toContain("sk-ax-v1-secret");
  expect(document.querySelector('dialog button[aria-label="关闭"]')).toBeNull();
  await pressEscape();
  await pressBackdrop();
  expect(closed).not.toHaveBeenCalled();
  act(() => button("完成").click());
  await settle();
  expect(closed).toHaveBeenCalledWith("sk-ax-v1-secret");
});

type Key = { name: string };

function KeyPanel({ save, close }: { save: (draft: Key) => Promise<Key>; close: () => void }) {
  const write = useExplicitSave({ initial: { name: "ci" }, save });
  return (
    <EditPanel
      title="编辑 key"
      write={write}
      onClose={() => {
        closed();
        close();
      }}
    >
      <input aria-label="名称" value={write.draft.name} onChange={(e) => write.set({ name: e.target.value })} />
    </EditPanel>
  );
}

it("locks the panel while saving, keeps the draft on a failure and sends the same request again", async () => {
  const api = fakeApi<Key>();
  render(<Opener overlay={(close) => <KeyPanel save={api.send} close={close} />} />);
  open();
  type(field(), "ci-runner");
  act(() => {
    button("保存").click();
    (document.querySelector("dialog form") as HTMLFormElement).requestSubmit();
  });
  expect(api.calls).toHaveLength(1);
  expect(field().matches(":disabled")).toBe(true);
  await pressEscape();
  await pressBackdrop();
  expect(dialog()?.open).toBe(true);

  await act(async () => api.calls[0]?.reject(new ApiError(503, "req_k7Hc2pQ9xWm4")));
  const failed = document.querySelector('dialog [role="alert"]')?.textContent;
  expect(failed).toContain("没有提交成功");
  expect(failed).toContain("你填的内容都还在");
  expect(failed).toContain("req_k7Hc2pQ9xWm4");
  expect(field().value).toBe("ci-runner");

  act(() => button("重试").click());
  expect(api.calls[1]?.body).toEqual(api.calls[0]?.body);
  await act(async () => api.calls[1]?.resolve({ name: "ci-runner" }));
  await settle();
  expect(dialog()).toBeNull();
  expect(closed).toHaveBeenCalledTimes(1);
});

it("says so when a versioned object moved on, and shows the latest one", async () => {
  const api = fakeApi<{ version: number; on: boolean }>();
  function Default() {
    const write = useLiveSave({
      initial: { version: 1, on: false },
      write: api.send,
      reread: () => Promise.resolve({ version: 4, on: false }),
    });
    return (
      <EditPanel title="路由默认" write={write} onClose={() => {}}>
        <input type="checkbox" checked={write.value.on} onChange={() => write.change((v) => ({ ...v, on: !v.on }))} />
      </EditPanel>
    );
  }
  render(<Opener overlay={() => <Default />} />);
  open();
  const box = document.querySelector("dialog input") as HTMLInputElement;
  act(() => box.click());
  expect(document.querySelector('dialog [role="status"]')?.textContent).toBe("正在保存");
  const stale = new ApiError(409, "req_1", { body: { error: { code: "stale_revision", message: "" } } });
  await act(async () => api.calls[0]?.reject(stale));
  await settle();
  expect(dialog()?.textContent).toContain("这一项刚在别处被改过");
  expect(box.checked).toBe(false);
  expect(api.calls).toHaveLength(1);
});

it.each(["explicit", "live"])("lets nothing close or delete beside a %s write that is out", async (mode) => {
  const api = fakeApi<Key>();
  const remove = vi.fn();
  function Panel() {
    const explicit = useExplicitSave({ initial: { name: "ci" }, save: api.send });
    const live = useLiveSave({ initial: { name: "ci" }, write: api.send, reread: api.send.bind(null, { name: "" }) });
    const write = mode === "explicit" ? explicit : live;
    return (
      <EditPanel
        title="编辑 key"
        write={write}
        start={
          <button type="button" onClick={remove}>
            {"删除"}
          </button>
        }
        onClose={closed}
      >
        <input
          aria-label="名称"
          defaultValue="ci"
          onChange={(e) =>
            mode === "live" ? live.change(() => ({ name: e.target.value })) : explicit.set({ name: e.target.value })
          }
        />
      </EditPanel>
    );
  }
  render(<Opener overlay={() => <Panel />} />);
  open();
  type(field(), "ci-runner");
  if (mode === "explicit") {
    act(() => button("保存").click());
  }
  expect(api.calls).toHaveLength(1);
  act(() => {
    button("删除").click();
    (document.querySelector('dialog button[aria-label="关闭"]') as HTMLButtonElement).click();
  });
  await pressEscape();
  await settle();
  expect(remove).not.toHaveBeenCalled();
  expect(closed).not.toHaveBeenCalled();
  expect(dialog()?.open).toBe(true);
  expect(field().matches(":disabled")).toBe(mode === "explicit");
});

function Disable({ reverse }: { reverse: () => Promise<unknown> }) {
  const undo = useUndo();
  return (
    <>
      <button type="button" onClick={() => undo.offer("已停用 ci", reverse)}>
        {"disable"}
      </button>
      {undo.failed}
    </>
  );
}

it.each([
  {
    case: "refused",
    error: new ApiError(400, "req_2", { body: { error: { code: "invalid_request" } } }),
    says: "撤销没有完成",
  },
  { case: "unanswered", error: new ApiError(0, null), says: "不确定撤销有没有生效" },
])("undoes once with a reverse request, and says what a $case one left", async ({ error, says }) => {
  const api = fakeApi<undefined, unknown>();
  const reverse = vi.fn(() => api.send(undefined));
  render(
    <ToastProvider>
      <Disable reverse={reverse} />
    </ToastProvider>,
  );
  act(() => button("disable").click());
  expect(document.querySelector('[role="status"]')?.textContent).toContain("已停用 ci");
  const undo = button("撤销");
  act(() => {
    undo.click();
    undo.click();
  });
  expect(reverse).toHaveBeenCalledTimes(1);
  await act(async () => api.calls[0]?.reject(error));
  expect(document.querySelector('[role="alert"]')?.textContent).toContain(says);
});

// The exit motion takes a moment; while it plays the dialog is still on the page, and nothing in it may act.
function exitPlaysOn() {
  vi.spyOn(Element.prototype, "getAnimations").mockReturnValue([
    { finished: new Promise(() => {}) } as unknown as Animation,
  ]);
}
const submit = () => act(() => (document.querySelector("dialog form") as HTMLFormElement).requestSubmit());

it("sends nothing from a dialog that is closing", async () => {
  const api = fakeApi<{ name: string }, string>();
  newKey(api.send);
  exitPlaysOn();
  type(field(), "ci");
  act(() => button("取消").click());
  submit();
  expect(api.calls).toHaveLength(0);
  expect(field().matches(":disabled")).toBe(true);
});

it("creates once when the dialog closes on success", async () => {
  const api = fakeApi<{ name: string }, string>();
  render(
    <Opener
      overlay={(close) => (
        <CreateDialog title="新建工作区" initial={{ name: "ci" }} create={api.send} onClose={close}>
          {(draft) => <input aria-label="名称" defaultValue={draft.name} />}
        </CreateDialog>
      )}
    />,
  );
  open();
  exitPlaysOn();
  submit();
  await act(async () => api.calls[0]?.resolve("ws_1"));
  expect(dialog()).not.toBeNull();
  submit();
  expect(api.calls).toHaveLength(1);
});

it("lets a toast go after its time, and holds it while pointed at", async () => {
  vi.useFakeTimers();
  function Say() {
    const say = useToast();
    return (
      <button type="button" onClick={() => say("已保存")}>
        {"say"}
      </button>
    );
  }
  render(
    <ToastProvider>
      <Say />
    </ToastProvider>,
  );
  act(() => button("say").click());
  const toast = () => document.querySelector('[role="status"]');
  act(() => toast()?.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
  await act(async () => vi.advanceTimersByTime(5000));
  expect(toast()).not.toBeNull();
  act(() => toast()?.dispatchEvent(new MouseEvent("mouseout", { bubbles: true })));
  await act(async () => vi.advanceTimersByTime(1600));
  expect(toast()).toBeNull();
});

it.each(["sheet", "toast", "state-block", "submit-failed"])("plays no motion of %s under reduced motion", (name) => {
  expect(reduced(moduleCss(name))).not.toMatch(/animation|transition/);
});

const query = (status: "pending" | "error" | "success", data?: string[], error: unknown = null) => ({
  status,
  data,
  error,
  errorUpdatedAt: 0,
  refetch: () => {},
});

it.each([
  { state: "loading", query: query("pending"), says: "正在加载" },
  { state: "empty", query: query("success", []), says: "这里还没有内容" },
  { state: "failed", query: query("error", undefined, new ApiError(500, "req_9")), says: "没能加载" },
  { state: "not allowed", query: query("error", undefined, new ApiError(403, null)), says: "没有权限" },
])("shows the design's state block when $state", ({ query, says }) => {
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <DataState query={query} isEmpty={(rows) => rows.length === 0}>
        {(rows) => rows.join()}
      </DataState>
    </I18nextProvider>,
  );
  expect(html).toContain(says);
  expect(html).toMatch(/class="_(root|placeholder)_/);
  expect(html).not.toMatch(/class="[^"]*\b(flex|text-|py-)/);
});
