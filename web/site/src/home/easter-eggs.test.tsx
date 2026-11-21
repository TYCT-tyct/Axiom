// @vitest-environment jsdom
import type { EggsOptions } from "@axiom/fx/eggs";
import type { Sea } from "@axiom/fx/sea";
import i18next from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { startI18n } from "../../../packages/ui/src/i18n";
import en from "../locales/en";
import zh from "../locales/zh";
import { SeaEggs } from "./easter-eggs";

// The terminal to the keyboard. The eggs' module (its "/" and the sea) is the end-to-end test's (e2e/fx-eggs.spec.ts):
// here it is a stand-in that hands over what the page gives it.
const started = vi.hoisted(() => ({ options: undefined as EggsOptions | undefined, feed: vi.fn() }));
vi.mock("@axiom/fx/eggs", () => ({
  startEggs: (_frame: HTMLElement, _sea: Sea, options: EggsOptions) => {
    started.options = options;
    return { feed: started.feed, milestone: vi.fn(), dive: vi.fn(), surface: vi.fn(), destroy: vi.fn() };
  },
}));

let host: HTMLElement;
let root: Root;
let before: HTMLButtonElement;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(console, "log").mockImplementation(() => {});
  // jsdom has no animations: a fade ends at once
  Element.prototype.animate = vi.fn(() => {
    const fade = { cancel: () => {} } as Animation;
    Object.defineProperty(fade, "onfinish", { set: (done: () => void) => done() });
    return fade;
  });
  Element.prototype.getAnimations = () => [];
  localStorage.setItem("axiom.language", "en");
  startI18n("site", { en, zh });
  host = document.createElement("div");
  host.className = "tr";
  host.append(Object.assign(document.createElement("div"), { className: "ho" }));
  before = document.createElement("button");
  document.body.append(host, before);
  root = createRoot(host.appendChild(document.createElement("div")));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  before.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const dialog = () => host.querySelector<HTMLElement>('[role="dialog"]');
const field = () => host.querySelector<HTMLInputElement>("input") as HTMLInputElement;
const lines = () => [...host.querySelectorAll('[aria-live="polite"] > div')].map((line) => line.textContent);
const press = (key: string) =>
  act(async () => {
    field().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });
async function type(line: string) {
  field().value = line;
  await press("Enter");
}

const mount = () =>
  act(async () =>
    root.render(
      <I18nextProvider i18n={i18next}>
        <SeaEggs frame={host} sea={{} as Sea} whale={() => {}} />
      </I18nextProvider>,
    ),
  );

it("opens with the focus in its input, runs what is typed, recalls it, and Escape closes it back to where the focus was", async () => {
  await mount();
  expect(dialog()?.hidden).toBe(true);
  expect(window.axiom).toBeDefined();
  before.focus();
  await act(async () => started.options?.terminal.show());
  expect(started.options?.terminal.open()).toBe(true);
  expect(dialog()?.hidden).toBe(false);
  expect(dialog()?.getAttribute("aria-label")).toBe("Sea-floor terminal");
  expect(document.activeElement).toBe(field());
  expect(lines()).toEqual(["Sea-floor terminal. Type help to see what it does, Esc to close"]);

  await type("feed");
  expect(started.feed).toHaveBeenCalledOnce();
  expect(lines().slice(-2)).toEqual(["$feed", "Here come the fish."]);
  await type("sudo");
  await press("ArrowUp");
  expect(field().value).toBe("sudo");
  await press("ArrowUp");
  expect(field().value).toBe("feed");
  await press("ArrowDown");
  await press("ArrowDown");
  expect(field().value).toBe("");

  await press("Escape");
  expect(started.options?.terminal.open()).toBe(false);
  expect(dialog()?.hidden).toBe(true);
  expect(document.activeElement).toBe(before);
});

it("keeps its last ten lines", async () => {
  await mount();
  await act(async () => started.options?.terminal.show());
  // opened from nowhere in particular, closing gives the focus back to the page, so the page's keys work again
  await press("Escape");
  expect(document.activeElement).toBe(document.body);
  await act(async () => started.options?.terminal.show());
  for (let i = 0; i < 8; i++) await type("whoami");
  expect(lines()).toHaveLength(10);
  expect(lines().at(-1)).toBe("One key for every model it may use.");
  await type("clear");
  expect(lines()).toEqual([]);
});
