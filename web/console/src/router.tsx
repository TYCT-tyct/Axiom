import { focusHeading, NotFoundState, routerDefaults } from "@axiom/ui";
import { createRoute, createRouter } from "@tanstack/react-router";
import { accountRoutes } from "./routes/account";
import { authRoutes } from "./routes/auth";
import { billingRoutes } from "./routes/billing";
import { keysRoutes } from "./routes/keys";
import { modelsRoutes } from "./routes/models";
import { app, root } from "./routes/shell";
import { usageRoutes } from "./routes/usage";
import type { RouterContext } from "./warm";

// Every page is its own chunk, loaded when the user is about to open it; the first screen loads the page of the address
// it opens (scripts/budget.ts counts it). Each area keeps its routes in a file of its own under routes/.
const routeTree = root.addChildren([
  authRoutes,
  app.addChildren([
    ...usageRoutes,
    ...keysRoutes,
    ...modelsRoutes,
    ...billingRoutes,
    ...accountRoutes,
    createRoute({ getParentRoute: () => app, path: "$", component: NotFoundState }),
  ]),
]);

// The pages' data lives in the query cache, which decides what is fresh: the router asks a page's loader every time.
export const router = createRouter({
  ...routerDefaults,
  routeTree,
  basepath: import.meta.env.BASE_URL,
  context: { queryClient: undefined as unknown as RouterContext["queryClient"] },
  defaultPreloadStaleTime: 0,
});

router.subscribe("onResolved", focusHeading);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
