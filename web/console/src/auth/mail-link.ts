import { useSyncExternalStore } from "react";

// A mailed link (a password reset, an address to verify) carries its single-use token in the fragment, which the
// browser does not send to the server (docs/frontend-api.md「注册与找回密码」). The link's route takes it out of the
// address before anything of the page loads (router.tsx mailLinkRoute), so a page that fails to load, or is left while
// it loads, leaves no token in the history; the token stays here, in memory, for the page (reset-password.tsx,
// verify-email.tsx). It belongs to the route that took it: the other route's page neither reads nor drops it. Each
// arrival with a fragment replaces it, one without a token included, and is a new link (n), even with the same token.

export type MailRoute = "/reset-password" | "/verify-email";
type Link = { token: string; n: number; route: MailRoute | null };

let link: Link = { token: "", n: 0, route: null };
const listeners = new Set<() => void>();

const set = (token: string, route: MailRoute) => {
  link = { token, n: link.n + 1, route };
  for (const listener of listeners) listener();
};

// keepMailLink takes the token from an address's fragment (without its "#"), for the route it arrived at.
export function keepMailLink(route: MailRoute, hash: string) {
  set(new URLSearchParams(hash).get("token") ?? "", route);
}

// dropMailLink forgets the route's token: the visitor asks for a new link, or leaves the page.
export function dropMailLink(route: MailRoute) {
  if (link.route === route) set("", route);
}

function onMailLink(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// useMailLink is the link the route's page holds: its token ("" for none) and which arrival it is.
export function useMailLink(route: MailRoute) {
  const now = useSyncExternalStore(onMailLink, () => link);
  return { token: now.route === route ? now.token : "", n: now.n };
}
