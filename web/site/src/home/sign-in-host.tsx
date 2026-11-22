import { consoleAuth } from "@axiom/api/auth";
import {
  AppProviders,
  type SessionNavigation,
  SessionNavigationProvider,
  type SessionTarget,
  safeNext,
} from "@axiom/app";
import {
  fetchSite,
  type LoginHost,
  LoginHostContext,
  type LoginLinkProps,
  type LoginNavigation,
  LoginNavigationProvider,
  type LoginPath,
  siteQuery,
} from "@axiom/login";
import { LoginLayout } from "@axiom/login/login-frame";
import { useQueryClient } from "@tanstack/react-query";
import {
  type ComponentType,
  type LazyExoticComponent,
  lazy,
  type MouseEvent,
  type ReactNode,
  Suspense,
  useMemo,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { siteAnswer } from "../shell";
import { consoleHref, type Place, placeOf } from "./sign-in-place";

// The sign-in pages, each loaded when first shown, but signing in and registering, which the homepage's buttons open:
// they are asked for with this module (preloadPages, sign-in.tsx), so a press shows its page at once.
const loads: Record<LoginPath, () => Promise<{ default: ComponentType }>> = {
  "/sign-in": () => import("@axiom/login/sign-in"),
  "/register": () => import("@axiom/login/register"),
  "/mfa": () => import("@axiom/login/mfa"),
  "/verify-email": () => import("@axiom/login/verify-email"),
  "/reset-password": () => import("@axiom/login/reset-password"),
  "/consent": () => import("@axiom/login/consent"),
};
const pages = Object.fromEntries(Object.entries(loads).map(([path, load]) => [path, lazy(load)])) as Record<
  LoginPath,
  LazyExoticComponent<ComponentType>
>;
export const preloadPages = () => Promise.all([loads["/sign-in"](), loads["/register"]()]);

const plainClick = (e: MouseEvent) => e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;

// The sign-in pages' navigation in the website's document: a step goes on to another in place, its address the
// console's (which a reload or a shared link opens in the console itself); a link is a real one, so a new tab works.
function useNavigation(place: Place, go: (to: Place, replace: boolean) => void): LoginNavigation {
  const Link = useMemo(
    () =>
      function SiteLoginLink({ to, search, onClick, ...props }: LoginLinkProps) {
        const target: Place = { path: to, search: { next: search?.next, invite: search?.invite } };
        const press = (e: MouseEvent<HTMLAnchorElement>) => {
          onClick?.(e);
          if (e.defaultPrevented || !plainClick(e)) return;
          e.preventDefault();
          go(target, false);
        };
        return <a {...props} href={consoleHref(target)} onClick={press} />;
      },
    [go],
  );
  return useMemo(
    () => ({
      search: place.search,
      navigate: (to, options) => go({ path: to, search: options?.search ?? {} }, options?.replace ?? false),
      Link,
    }),
    [place.search, go, Link],
  );
}

// The gate's ways on: another sign-in step stays in this document; the console (a member, or a step the website does
// not hold) is entered with a page load, which the view transition joins (crossing.ts).
function useSession(place: Place, go: (to: Place, replace: boolean) => void): SessionNavigation {
  return useMemo(
    () => ({
      here: consoleHref(place).replace(/^\/console/, ""),
      next: place.search.next,
      replace: (path: string, target: SessionTarget) => {
        const step = target === "auth" ? placeOf(new URL(`/console${path}`, window.location.origin)) : null;
        if (step) go(step, true);
        else window.location.replace(`/console${path}`);
      },
    }),
    [place, go],
  );
}

export type SignInHostProps = {
  // where the sign-in page stands: a box over the homepage's frame (sign-in.tsx) or over the models page
  mount: HTMLElement;
  // the page holding the frame (LoginHost): the homepage, whose sea the frame stands on, the tiles transition joining
  // them, or the models page, where the frame brings its own sea and comes in as an arrival does (models-sign-in.tsx)
  host: LoginHost;
  // the page on show; none while the frame waits, ready, for a press (its sea, its pitch and its switch laid out)
  place: Place | null;
  go: (to: Place, replace: boolean) => void;
};

// the page a waiting frame's navigation stands for: nothing reads it until a page is shown
const waiting: Place = { path: "/sign-in", search: {} };

// the sign-in page's question, shown once a press chose it. Its gate asks for the session then, not while the frame
// waits: most visitors never press, and the transition writes the question 620 ms in, time enough for the answer.
function Question({ place }: { place: Place | null }) {
  const Page = place ? pages[place.path] : null;
  return Page ? (
    <Suspense fallback={null}>
      <Page />
    </Suspense>
  ) : null;
}

// The page has asked for the site already (shell.tsx): the frame and its pages read that answer, still out or come,
// rather than ask again; a call that did not answer leaves them to ask themselves.
function SameSite({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  useState(() => {
    const answer = siteAnswer();
    if (answer)
      void client.prefetchQuery({ ...siteQuery, queryFn: ({ signal }) => answer.catch(() => fetchSite(signal)) });
  });
  return children;
}

// SignInHost is the console's sign-in pages in the website's document, with the console's session and requests: the
// Login board over the homepage's sea (LoginHost), so the tiles transition between the two is one scene, or over the
// models page on a sea of its own.
export function SignInHost({ mount, host, place: read, go }: SignInHostProps) {
  // where to go on from here is the console's own address, never another site's: checked as the console checks it
  const place = useMemo(
    () => (read ? { ...read, search: { ...read.search, next: safeNext(read.search.next, "/console/") } } : null),
    [read],
  );
  const navigation = useNavigation(place ?? waiting, go);
  const session = useSession(place ?? waiting, go);
  return createPortal(
    <AppProviders api={consoleAuth} requiresMfa={false} requiresConsent>
      <SessionNavigationProvider value={session}>
        <LoginNavigationProvider value={navigation}>
          <LoginHostContext value={host}>
            <SameSite>
              <LoginLayout>
                <Question place={place} />
              </LoginLayout>
            </SameSite>
          </LoginHostContext>
        </LoginNavigationProvider>
      </SessionNavigationProvider>
    </AppProviders>,
    mount,
  );
}
