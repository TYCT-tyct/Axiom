// The reset email's link carries its single-use token in the fragment, which the browser does not send to the server
// (docs/frontend-api.md「注册与找回密码」). The reset route takes it out of the address before anything of the page loads
// (router.tsx), so a page that fails to load, or is left while it loads, leaves no token in the history; the token
// stays here, in memory, for the page (reset-password.tsx). Each link taken is a new one (n), even with the same token.

type Link = { token: string; n: number };

let link: Link = { token: "", n: 0 };
const listeners = new Set<() => void>();

const set = (token: string) => {
  link = { token, n: link.n + 1 };
  for (const listener of listeners) listener();
};

// keepResetLink takes the token from an address's fragment (without its "#"); a fragment without one changes nothing.
export function keepResetLink(hash: string) {
  const token = new URLSearchParams(hash).get("token");
  if (token) set(token);
}

// dropResetLink forgets the token: the visitor asks for a new link, or leaves the page.
export const dropResetLink = () => set("");

export const resetLink = () => link;

export function onResetLink(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
