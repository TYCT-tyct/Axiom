import { focusPage, NotFoundState, routerDefaults } from "@axiom/ui";
import { createRoute, createRouter } from "@tanstack/react-router";
import { authRoutes } from "./routes/auth";
import { peopleRoutes } from "./routes/people";
import { recordsRoutes } from "./routes/records";
import { runRoutes } from "./routes/run";
import { systemRoutes } from "./routes/settings";
import { app, root } from "./routes/shell";
import { supplyRoutes } from "./routes/supply";

// Each group of the navigation (nav.ts) keeps its routes in a file of its own under routes/.
const routeTree = root.addChildren([
  ...authRoutes,
  app.addChildren([
    ...runRoutes,
    ...peopleRoutes,
    ...supplyRoutes,
    ...recordsRoutes,
    ...systemRoutes,
    createRoute({ getParentRoute: () => app, path: "$", component: NotFoundState }),
  ]),
]);

export const router = createRouter({ ...routerDefaults, routeTree, basepath: import.meta.env.BASE_URL });

router.subscribe("onResolved", focusPage);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
