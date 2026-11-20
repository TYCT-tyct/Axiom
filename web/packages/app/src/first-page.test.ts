import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
} from "@tanstack/react-router";
import { expect, it, vi } from "vitest";
import { preloadFirstPage } from "./first-page";

// The page's code is asked for before React renders anything, under the app's base path, and only the opened page's.
it("asks for the opened page's code before anything renders", () => {
  const never = () => new Promise<{ default: () => null }>(() => undefined);
  const keys = vi.fn(never);
  const other = vi.fn(never);
  const root = createRootRoute();
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({ getParentRoute: () => root, path: "/keys", component: lazyRouteComponent(keys) }),
      createRoute({ getParentRoute: () => root, path: "/other", component: lazyRouteComponent(other) }),
    ]),
    basepath: "/console/",
    history: createMemoryHistory({ initialEntries: ["/console/keys"] }),
  });
  preloadFirstPage(router);
  expect(keys).toHaveBeenCalledOnce();
  expect(other).not.toHaveBeenCalled();
});
