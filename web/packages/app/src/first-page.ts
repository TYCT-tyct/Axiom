import type { AnyRouter } from "@tanstack/react-router";

// preloadFirstPage asks for the code of the page the address opens as soon as the app's router exists. Left to the
// router, the request waits for React's first render, which on a phone at the budget's 4x CPU comes some 300 ms after
// the app's own code arrived, with the network idle meanwhile; the page's chunk was then the last thing the first paint
// waited for (#747). A chunk that fails to load fails again, and is reported, when the router loads the page.
export function preloadFirstPage(router: AnyRouter) {
  const [routes] = router.getMatchedRoutes(router.state.location.pathname);
  for (const route of routes) {
    router.loadRouteChunk(route)?.catch(() => undefined);
  }
}
