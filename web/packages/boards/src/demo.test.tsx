// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";

const demo = vi.hoisted(() => ({ mounted: () => {}, unmount: () => {}, loaded: [] as string[] }));
vi.mock("react-dom/client", async (original) => {
  const client = await original<typeof import("react-dom/client")>();
  return {
    createRoot: (...args: Parameters<typeof client.createRoot>) => {
      const root = client.createRoot(...args);
      demo.unmount = () => root.unmount();
      return root;
    },
  };
});
vi.mock("./Login/Board.tsx", () => {
  demo.loaded.push("Login");
  return { default: () => <div data-board="Login" ref={() => demo.mounted()} /> };
});
vi.mock("./AxApp/Board.tsx", () => {
  demo.loaded.push("AxApp");
  return { default: () => <div data-board="AxApp" /> };
});

afterEach(() => {
  demo.unmount();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

it("mounts the selected route while the animation clock is paused", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
  vi.stubGlobal("location", new URL("http://localhost/Login?theme=dark"));
  Object.defineProperty(document, "fonts", { configurable: true, value: { load: async () => [] } });
  document.body.innerHTML = '<div id="root"></div>';
  const mounted = new Promise<void>((resolve) => {
    demo.mounted = resolve;
  });
  // Do not use act or advance timers: either would hide a Suspense reveal waiting on the frozen clock.
  await import("../demo/main.tsx");
  await mounted;
  expect(document.querySelector("#root > div")?.getAttribute("data-board")).toBe("Login");
  expect(demo.loaded).toEqual(["Login"]);
});
