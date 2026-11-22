import type { LoginPath, LoginSearch } from "@axiom/login";

// Where the sign-in pages stand when the website holds them: one of the console's sign-in steps, with its address's
// query. The address is the console's own (/console/sign-in?next=…), so a reload or a shared link opens the console.
export type Place = { path: LoginPath; search: LoginSearch };

const paths: readonly LoginPath[] = ["/sign-in", "/register", "/mfa", "/verify-email", "/reset-password", "/consent"];
const mount = "/console";

// placeOf reads a sign-in step from an address: null for any other (the homepage, a console page). The query's next is
// read as it is; the sign-in host checks it (safeNext) before anything follows it.
export function placeOf(url: URL): Place | null {
  if (url.origin !== window.location.origin || !url.pathname.startsWith(`${mount}/`)) return null;
  const path = url.pathname.slice(mount.length).replace(/\/$/, "") as LoginPath;
  if (!paths.includes(path)) return null;
  const read = (key: keyof LoginSearch) => url.searchParams.get(key) ?? undefined;
  return { path, search: { next: read("next"), invite: read("invite"), oauth: read("oauth") } };
}

// signingIn is whether the frame shows the sign-in page, or is on its way there or back (the transition's state): the
// homepage's hidden keys and moments wait while it does
export const signingIn = (frame: HTMLElement) => frame.dataset.state === "login" || frame.dataset.state === "run";

// consoleHref is a step's address: the console's path and the query it keeps.
export function consoleHref({ path, search }: Place) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(search)) if (value) query.set(key, value);
  const tail = query.toString();
  return `${mount}${path}${tail ? `?${tail}` : ""}`;
}
