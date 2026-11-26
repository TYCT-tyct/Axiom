import { modelAbilities } from "@axiom/ui";
import { createRoute, lazyRouteComponent } from "@tanstack/react-router";
import { type ModelFilters, modelsQuery } from "../queries";
import { ask, warming } from "../warm";
import { app, text } from "./shell";

// The models and their prices.
export const modelsRoutes = [
  createRoute({
    getParentRoute: () => app,
    path: "/models",
    validateSearch: (search: Record<string, unknown>) => ({
      q: text(search.q),
      author: text(search.author),
      ability: modelAbility(search.ability),
    }),
    // the page reads the whole of its search as its filters
    loaderDeps: ({ search }) => search,
    loader: warming(async () => (client, _session, filters: ModelFilters) => ask(client, [modelsQuery(filters)])),
    component: lazyRouteComponent(() => import("../pages/models")),
  }),
] as const;

// An ability from the address is one the contract names; anything else is no filter, not a request it would refuse.
function modelAbility(value: unknown) {
  return typeof value === "string" && (modelAbilities as readonly string[]).includes(value)
    ? (value as (typeof modelAbilities)[number])
    : undefined;
}
