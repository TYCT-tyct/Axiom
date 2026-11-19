import type { Prices } from "./public-models";
import type { Join } from "./site-context";

// Where the home page's links go. Signing in and registering are the console's pages (port design §9), which the
// home page leaves for through the tiles departure (behaviour.ts useDeparture); prices are on the models page.
export const links = {
  home: "/",
  models: "/models",
  pricing: "/models",
  signIn: "/console/sign-in",
  signUp: "/console/register",
};

// Where a way in leads by how the site takes new users (getSite's registration): to registering, or, on a site that takes
// nobody new or that has not said yet, to signing in, where an existing account gets its keys.
export const joinHref = (join: Join) => (join === "open" || join === "invite" ? links.signUp : links.signIn);

// Where the prices are: the models page publishes them, unless the site keeps them for its users, who see them on the
// console's models page once signed in. Until that is known, the models page, which says which it is.
export const pricesHref = (prices: Prices) => (prices === "members" ? "/console/models" : links.pricing);
