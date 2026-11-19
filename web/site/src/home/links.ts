import type { Registration } from "@axiom/api";

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
// nobody new, to signing in, where an existing account gets its keys. Until the site has answered, to registering, as
// the board draws it; the register page says what the site does.
export const joinHref = (registration: Registration | undefined) =>
  registration === "closed" ? links.signIn : links.signUp;
