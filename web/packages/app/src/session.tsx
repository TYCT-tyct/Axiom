import { ApiError, type Schemas, setCsrfToken } from "@axiom/api";
import type { AuthApi } from "@axiom/api/auth";
import { DataState } from "@axiom/ui";
import { type QueryClient, queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, use, useCallback, useEffect, useRef, useState } from "react";
import { type SessionTarget, useSessionNavigation } from "./navigation";

export type Session = Schemas["Session"];

// Where a visitor stands: nobody signed in (guest), a password proven and a second factor awaited (pending), a password
// proven where two-step verification is required and not yet set up (enroll), current legal documents not accepted
// (consent), or in (member). The server decides; the pages only route by it.
type Stage = "guest" | "pending" | "enroll" | "consent" | "member";

// What a surface needs from the app: its Session calls, whether its operations need two-step verification (the admin's
// all do, the console's do not), and whether members must accept the current legal documents (the console does).
type Auth = { api: AuthApi; requiresMfa: boolean; requiresConsent: boolean };

const AuthContext = createContext<Auth | null>(null);

export function AuthProvider({ api, requiresMfa, requiresConsent = false, children }: Auth & { children: ReactNode }) {
  return <AuthContext value={{ api, requiresMfa, requiresConsent }}>{children}</AuthContext>;
}

export function useAuth() {
  const auth = use(AuthContext);
  if (!auth) {
    throw new Error("useAuth is used outside an AuthProvider");
  }
  return auth;
}

const sessionKey = ["session"] as const;

// Everything under this key belongs to one account's session and goes when it ends or passes to another account, so
// the next person to sign in on this browser never sees it.
export const accountKey = "account";

// Whose session it is. The cookie is shared by every tab of the browser, so another tab can end one person's session and
// start another's while this page is open; the session this page then reads is the other's.
export const identityOf = (session: Session | null) => (session ? `${session.user.id}/${session.account}` : "");

// sessionBinding names the session itself, not just the person: the CSRF token is minted with the session, so an answer
// that started under one session is not shown under the next one of the same person.
export function sessionBinding(session: Session | null): string {
  return session ? `${identityOf(session)}/${session.csrf_token}` : "";
}

// currentSession is the session the cache holds now, without fetching: work that started earlier reads it before showing
// its answer, because the person or the session may have changed while the request was out.
export function currentSession(client: QueryClient): Session | null {
  return client.getQueryData<Session | null>(sessionKey) ?? null;
}

// useCachedSession is the same as currentSession for a component that must react to the change, such as the recovery
// codes host: it does not fetch, only follows what the session query already holds.
export function useCachedSession(): Session | null {
  const { api } = useAuth();
  return useQuery({ ...sessionQuery(api), enabled: false }).data ?? null;
}

// SessionClaim names the session an answer belongs to: the person the page showed and the session it asked under, both
// read before the request went out.
export type SessionClaim = { binding: string; identity: string };

export function claimSession(client: QueryClient): SessionClaim | null {
  const session = currentSession(client);
  return session ? { binding: sessionBinding(session), identity: identityOf(session) } : null;
}

// holdsSession reports whether an answer claimed by binding still belongs on the page: the same person and the same
// session, not the next one of the same person.
export function holdsSession(client: QueryClient, binding: string): boolean {
  return binding !== "" && sessionBinding(currentSession(client)) === binding;
}

// holdsIdentity reports whether an answer claimed by identity still belongs on the page: the same person, whatever
// session they hold now. A factor change rotates the session before its answer is read, so its answer compares the
// person, not the token.
export function holdsIdentity(client: QueryClient, identity: string): boolean {
  return identity !== "" && identityOf(currentSession(client)) === identity;
}

// endUnauthenticated ends the session when a call was refused for want of one and the call belongs to the session still
// on the page. binding is the session the call started under: the browser's session is shared by every tab, so an answer
// can arrive after another tab signed in, and it must not end the session that replaced the one that asked. Queries and
// mutations reach it through their caches; work outside them, such as a file download, calls it too.
export function endUnauthenticated(client: QueryClient, error: unknown, binding: string) {
  if (error instanceof ApiError && error.code === "unauthenticated" && holdsSession(client, binding)) {
    endSession(client);
  }
}

// useSessionBound runs work for the session that is current when it starts. The work gets a signal that ends when what
// the answer belongs to ends, changes, or the page goes away; an answer that arrives after either is dropped instead of
// handed to the caller. scope is what the answer belongs to: the exact session that asked, or the person (a factor
// change replaces that person's session with a new one, so its answer cannot be tied to the token it was asked under).
// The caller also gets the claim, so something only one person may see is labelled with the person who asked, not
// whoever is on the page when it lands.
export function useSessionBound(scope: "session" | "identity" = "session") {
  const client = useQueryClient();
  const held = useCachedSession();
  const belonging = scope === "identity" ? identityOf(held) : sessionBinding(held);
  const running = useRef(new Set<AbortController>());
  useEffect(() => {
    return () => {
      if (!belonging) {
        return;
      }
      for (const controller of running.current) {
        controller.abort();
      }
      running.current.clear();
    };
  }, [belonging]);
  return useCallback(
    async <T,>(
      request: (signal: AbortSignal) => Promise<T>,
    ): Promise<{ value: T; claim: SessionClaim } | undefined> => {
      const claim = claimSession(client);
      if (!claim) {
        return undefined;
      }
      const controller = new AbortController();
      running.current.add(controller);
      try {
        const value = await request(controller.signal);
        const belongs =
          scope === "identity" ? holdsIdentity(client, claim.identity) : holdsSession(client, claim.binding);
        return belongs ? { value, claim } : undefined;
      } catch (error) {
        if (controller.signal.aborted) {
          return undefined;
        }
        endUnauthenticated(client, error, claim.binding);
        throw error;
      } finally {
        running.current.delete(controller);
      }
    },
    [client, scope],
  );
}

// What the session may do. A change of it without a change of person keeps the pages (a dialog may hold what is shown
// once) but not the data the lost permission read.
const permissionsOf = (session: Session | null) => (session ? [...session.permissions].sort().join(",") : "");

async function fetchSession(api: AuthApi, signal: AbortSignal) {
  try {
    const session = await api.session(signal);
    setCsrfToken(session.csrf_token);
    return session;
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      setCsrfToken("");
      return null;
    }
    throw error;
  }
}

// The current session, or null when nobody is signed in. A 401 is the answer "no session", not a failure, and the page
// keeps the CSRF token it carries in memory (@axiom/api) because the cookie itself is HttpOnly. A session of someone
// else than the one before it drops what the previous one loaded, and the requests still out for it: the same cache key
// would otherwise show one person's balance under another's name. The same person with other permissions keeps the pages
// and asks again for what is on them: what the account may no longer read is refused and shown as such, not kept.
function sessionQuery(api: AuthApi) {
  return queryOptions({
    queryKey: sessionKey,
    queryFn: async ({ client, signal }) => {
      const before = client.getQueryData<Session | null>(sessionKey) ?? null;
      const session = await fetchSession(api, signal);
      if (identityOf(session) !== identityOf(before)) {
        await client.cancelQueries({ queryKey: [accountKey] });
        client.removeQueries({ queryKey: [accountKey] });
      } else if (permissionsOf(session) !== permissionsOf(before)) {
        client.removeQueries({ queryKey: [accountKey], type: "inactive" });
        void client.invalidateQueries({ queryKey: [accountKey] });
      }
      return session;
    },
    refetchOnWindowFocus: "always",
  });
}

// endSession drops the session and what it loaded. Pages see the null session and send the visitor to sign in again,
// to come back to the page they were on.
function endSession(queryClient: QueryClient) {
  setCsrfToken("");
  queryClient.setQueryData(sessionKey, null);
  queryClient.removeQueries({ queryKey: [accountKey] });
}

// useReadSession reads the session the cookie holds now, and is how a page confirms it after a request that may have
// changed it: a read still out was sent with the cookie from before, and the cache would hand it back for this one,
// so it is cancelled first. What it reads is the session every page then sees.
export function useReadSession() {
  const queryClient = useQueryClient();
  const { api } = useAuth();
  return useCallback(async () => {
    await queryClient.cancelQueries({ queryKey: sessionKey });
    return await queryClient.fetchQuery({ ...sessionQuery(api), staleTime: 0 });
  }, [queryClient, api]);
}

// useStartSession adopts the CSRF token of a new session and loads it, returning what it loaded so a page that has just
// shown something from the change (recovery codes) can use the new session's token.
export function useStartSession() {
  const read = useReadSession();
  return async (csrfToken: string) => {
    setCsrfToken(csrfToken);
    return await read();
  };
}

// The session starts from the response, which set the cookie and carries the CSRF token; whoever reads the session
// query then sees the gate move the visitor on, to the second step or to the page they came from.
export function useSignIn() {
  const { api } = useAuth();
  const start = useStartSession();
  return useMutation({
    mutationFn: api.signIn,
    onSuccess: (grant) => start(grant.csrf_token),
  });
}

// A second step answers a 401 for its own pending sign-in being over, which the step ends itself (useEndPending): the
// cache's rule that a 401 ends the session on the page (providers.tsx) would leave the cookie naming it.
export function useVerifyMfa() {
  const { api } = useAuth();
  const adopt = useAdoptGrant();
  return useMutation({
    mutationFn: async (body: Schemas["MfaRequest"]) => adopt((await api.verifyMfa(body)).csrf_token),
    meta: { pendingStep: true },
  });
}

// UnreadSession is a second step that passed, whose new session could not then be read: the cookie is already the
// new session, so the step is over and is not offered again (a second code would be refused for want of a pending
// session, and that refusal would sign the new session out); only the read is tried again (useAdoptGrant).
export class UnreadSession extends Error {
  constructor(
    readonly csrf: string,
    readonly reason: unknown,
  ) {
    super("the session the second step started could not be read");
  }
}

// useAdoptGrant starts the session a second step's grant names, and answers a failed read as UnreadSession.
export function useAdoptGrant() {
  const start = useStartSession();
  return async (csrf: string) => {
    try {
      return await start(csrf);
    } catch (reason) {
      throw new UnreadSession(csrf, reason);
    }
  };
}

// pendingOver reports a second step's answer that the pending sign-in itself is over: its attempts are spent, or it
// expired. No code or passkey can finish it any more, even a right one; a wrong code is invalid_code instead.
export const pendingOver = (error: unknown) =>
  error instanceof ApiError && error.status === 401 && error.code === "unauthenticated";

// A pending sign-in that is over is signed out by its step (useEndPending), so the cookie holds no session and a
// reload lands on the sign-in page, never on a step that cannot be finished; that page says why the visitor is back
// (useSignInEnded). It reads the session again first and signs out only the pending session the step was for (its
// sessionBinding): a cookie that holds no session any more (it expired) has nothing to sign out, and still ended; one
// that holds another session (the step passed, or the person signed in again elsewhere) is kept, and nothing ended.
// A read or a sign-out that fails is thrown, for the step to say and offer again: the sign-in is still over.
let ended = false;

export function useEndPending() {
  const read = useReadSession();
  const signOut = useSignOut();
  return async (pending: string) => {
    // set before the read: a read that finds no session moves the gate to the sign-in page at once
    ended = true;
    try {
      const now = await read();
      if (now === null) return;
      if (sessionBinding(now) !== pending) {
        ended = false;
        return;
      }
      await signOut.mutateAsync();
    } catch (error) {
      // a sign-out refused for want of a session ends it on the page all the same (providers.tsx)
      if (!pendingOver(error)) ended = false;
      throw error;
    }
  };
}

export function useSignInEnded() {
  const [shown] = useState(() => ended);
  useEffect(() => {
    ended = false;
  }, []);
  return shown;
}

// A sign-out's answer expired the cookie, so the browser has no session: a read of it still out from before must not
// bring the old one back.
export function useSignOut() {
  const queryClient = useQueryClient();
  const { api } = useAuth();
  return useMutation({
    mutationFn: api.signOut,
    onSuccess: async () => {
      await queryClient.cancelQueries({ queryKey: sessionKey });
      endSession(queryClient);
    },
  });
}

const SessionContext = createContext<Session | null>(null);

export function useSession() {
  const session = use(SessionContext);
  if (!session) {
    throw new Error("useSession is used outside a signed-in page");
  }
  return session;
}

export function can(session: Session, permission: string) {
  return session.permissions.includes(permission);
}

function stageOf(session: Session | null, requiresMfa: boolean, requiresConsent: boolean): Stage {
  if (!session) {
    return "guest";
  }
  if (session.level === "pending_mfa") {
    return "pending";
  }
  if (requiresMfa && session.level !== "mfa") {
    return "enroll";
  }
  return requiresConsent && (session.consent?.required.length ?? 0) > 0 ? "consent" : "member";
}

// The origin the address is resolved against: only its being the same one for the base and for the target matters, so a
// target that resolves elsewhere (`//host`, `\\host`, a scheme) is told apart without reading window.location.
const origin = "http://app.invalid";
const guestPage = /^\/(sign-in|mfa|register|enroll|consent)(?![\w-])/;

// A path of this app, to return to after signing in, as the browser would resolve it under the app's base: an encoded
// or plain `..` that climbs out of the base, or an address of another origin, is dropped, and the way back is returned
// in its resolved form. A sign-in or consent page is dropped too: a way back to it would only loop. `base` is the app's, with the
// trailing slash.
const emailChangeTokenParam = "email-change-token";
const emailChangeStorageKey = "axiom:email-change-token";
let memoryEmailChangeToken: string | null = null;

function stripEmailTokenHash(rawHash: string): { token: string | null; cleanHash: string } {
  if (!rawHash.startsWith("#")) {
    return { token: null, cleanHash: rawHash };
  }
  const params = new URLSearchParams(rawHash.slice(1));
  if (!params.has(emailChangeTokenParam)) {
    return { token: null, cleanHash: rawHash };
  }
  const token = params.get(emailChangeTokenParam)?.trim() ?? "";
  params.delete(emailChangeTokenParam);
  const rest = params.toString();
  return { token: token || null, cleanHash: rest ? `#${rest}` : "" };
}

// Take `#email-change-token=...` out of an in-app path before putting it in `?next=` so confirmation tokens never enter
// query strings sent to the server on navigation or refresh.
export function stripEmailChangeToken(href: string): { token: string | null; cleanHref: string } {
  const hashIndex = href.indexOf("#");
  if (hashIndex === -1) {
    return { token: null, cleanHref: href };
  }
  const { token, cleanHash } = stripEmailTokenHash(href.slice(hashIndex));
  return { token, cleanHref: href.slice(0, hashIndex) + cleanHash };
}

export function stashEmailChangeToken(token: string) {
  const trimmed = token.trim();
  if (!trimmed) {
    return;
  }
  memoryEmailChangeToken = trimmed;
  try {
    window.sessionStorage.setItem(emailChangeStorageKey, trimmed);
  } catch {
    // Keep the token in tab memory when sessionStorage is blocked.
  }
}

export function consumeEmailChangeToken(): string | null {
  let stored: string | null = null;
  try {
    stored = window.sessionStorage.getItem(emailChangeStorageKey);
    window.sessionStorage.removeItem(emailChangeStorageKey);
  } catch {
    // Fall back to the in-memory token below.
  }
  const token = (stored ?? memoryEmailChangeToken ?? "").trim();
  memoryEmailChangeToken = null;
  return token || null;
}

export function safeNext(value: unknown, base = import.meta.env.BASE_URL) {
  if (typeof value !== "string" || !value.startsWith("/")) {
    return undefined;
  }
  const root = base.replace(/\/$/, "");
  let target: URL;
  try {
    target = new URL(root + value, origin);
  } catch {
    return undefined; // `//[` and the like do not parse
  }
  const inApp = target.origin === origin && (target.pathname === root || target.pathname.startsWith(`${root}/`));
  const path = target.pathname.slice(root.length) || "/";
  // A path that begins with two slashes, as `/a/..//host` resolves to, would be read as another site's once the history
  // puts the base (empty at the root) in front of it.
  return inApp && /^\/(?![/\\])/.test(path) && !guestPage.test(path)
    ? stripEmailChangeToken(path + target.search + target.hash).cleanHref
    : undefined;
}

// usePrefetchSession asks who is signed in ahead of a page that will gate on it (a sign-in page made ready before it is
// shown), so its gate answers at once; nothing is shown or redirected from it.

// Replace moves to a path of this app via the host's navigation adapter, so one gate serves hosts with different
// routers (or an external host that enters the app with a full page load).
function Replace({ path, target, stashToken }: { path: string; target: SessionTarget; stashToken?: string | null }) {
  const { replace } = useSessionNavigation();
  useEffect(() => {
    if (stashToken) {
      stashEmailChangeToken(stashToken);
    }
    replace(path, target);
  }, [replace, path, target, stashToken]);
  return null;
}

const guestPages = { guest: "/sign-in", pending: "/mfa", enroll: "/enroll", consent: "/consent" } as const;

export function redirectPath(
  stage: Stage,
  page: Stage,
  here: string,
  next?: string,
): { path: string; token: string | null } {
  const { token: fromToken, cleanHref: cleanFrom } = stripEmailChangeToken(here);
  const nextParts = page !== "member" && next ? stripEmailChangeToken(next) : undefined;
  const token = fromToken ?? nextParts?.token ?? null;
  if (stage === "member") {
    return { path: nextParts?.cleanHref ?? "/", token: null };
  }
  const targetNext = page === "member" ? cleanFrom : nextParts?.cleanHref;
  const path = targetNext ? `${guestPages[stage]}?next=${encodeURIComponent(targetNext)}` : guestPages[stage];
  return { path, token };
}

// A visitor in the wrong stage goes to the page of the stage they are in, remembering where they were going; a member
// who arrived on a guest page goes on to where they were going. Where they were going is the page the gate turned them
// away from, taken once: until the router has shown its first page, `here` is the address it is moving to, which this
// redirect itself changes, so reading it again would put that address in `next`, redirect again, and never stop.
function Redirect({ stage, page, here }: { stage: Stage; page: Stage; here: string }) {
  // Only a sign-in flow page validates its `next` (guestSearch); a protected page's is part of its own address.
  const { next: inherited } = useSessionNavigation();
  const [from] = useState(here);
  const { path, token } = redirectPath(stage, page, from, inherited);
  return <Replace path={path} target={stage === "member" ? "member" : "auth"} stashToken={token} />;
}

// Gate shows its page only to a visitor in the stage the page is for; anyone else is sent where they belong. A member
// whose session ends while a page is open lands on the sign-in page and comes back to that page after signing in. Page
// state is the account's too: a session of another account starts the pages afresh.
export function Gate({ stage, children }: { stage: Stage; children: ReactNode }) {
  const { api, requiresMfa, requiresConsent } = useAuth();
  const result = useQuery(sessionQuery(api));
  // A refresh that fails leaves the session the page already has in place: the server ends a session by saying so (401),
  // and a page open on something the user must act on, such as recovery codes, is not taken away for a bad moment.
  const session = result.data === undefined ? result : { ...result, status: "success" as const };
  // The page on screen, not the address the router is moving to: while the next page loads the current one stays, and
  // a gate that read the new address would redirect again from there.
  const { here } = useSessionNavigation();
  return (
    <DataState query={session} page>
      {(data) =>
        stageOf(data, requiresMfa, requiresConsent) === stage ? (
          <SessionContext key={identityOf(data)} value={data}>
            {children}
          </SessionContext>
        ) : (
          <Redirect stage={stageOf(data, requiresMfa, requiresConsent)} page={stage} here={here} />
        )
      }
    </DataState>
  );
}
