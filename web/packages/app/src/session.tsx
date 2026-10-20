import { ApiError, type Schemas, setCsrfToken } from "@axiom/api";
import type { AuthApi } from "@axiom/api/auth";
import { DataState } from "@axiom/ui";
import { type QueryClient, queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter, useRouterState, useSearch } from "@tanstack/react-router";
import { createContext, type ReactNode, use, useEffect, useState } from "react";

export type Session = Schemas["Session"];

// Where a visitor stands: nobody signed in (guest), a password proven and a second factor awaited (pending), a password
// proven where two-step verification is required and not yet set up (enroll), or in (member). The server decides; the
// pages only route by it.
type Stage = "guest" | "pending" | "enroll" | "member";

// What a surface needs from the app: its Session calls, and whether its operations need two-step verification (the
// admin's all do, the console's do not).
type Auth = { api: AuthApi; requiresMfa: boolean };

const AuthContext = createContext<Auth | null>(null);

export function AuthProvider({ api, requiresMfa, children }: Auth & { children: ReactNode }) {
  return <AuthContext value={{ api, requiresMfa }}>{children}</AuthContext>;
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
const identityOf = (session: Session | null) => (session ? `${session.user.id}/${session.account}` : "");

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
export function endSession(queryClient: QueryClient) {
  setCsrfToken("");
  queryClient.setQueryData(sessionKey, null);
  queryClient.removeQueries({ queryKey: [accountKey] });
}

// useStartSession adopts the CSRF token of a new session and loads it.
export function useStartSession() {
  const queryClient = useQueryClient();
  const { api } = useAuth();
  return async (csrfToken: string) => {
    setCsrfToken(csrfToken);
    await queryClient.fetchQuery({ ...sessionQuery(api), staleTime: 0 });
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

export function useVerifyMfa() {
  const { api } = useAuth();
  const start = useStartSession();
  return useMutation({
    mutationFn: api.verifyMfa,
    onSuccess: (grant) => start(grant.csrf_token),
  });
}

export function useSignOut() {
  const queryClient = useQueryClient();
  const { api } = useAuth();
  return useMutation({
    mutationFn: api.signOut,
    onSuccess: () => endSession(queryClient),
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

function stageOf(session: Session | null, requiresMfa: boolean): Stage {
  if (!session) {
    return "guest";
  }
  if (session.level === "pending_mfa") {
    return "pending";
  }
  return requiresMfa && session.level !== "mfa" ? "enroll" : "member";
}

// A path of this app, to return to after signing in. Anything else, such as another site's address, is dropped: the
// sign-in page must not be a way to send a visitor elsewhere, and a way back to a sign-in page would only loop.
export function safeNext(value: unknown) {
  const isGuestPage = /^\/(sign-in|mfa|register|enroll)(?![\w-])/;
  return typeof value === "string" && /^\/(?![/\\])/.test(value) && !isGuestPage.test(value) ? value : undefined;
}

// Replace moves to a path of this app. The history takes the app's base in front of the path, and no route is named, so
// one gate serves apps whose routes differ.
function Replace({ path }: { path: string }) {
  const router = useRouter();
  useEffect(() => {
    router.history.replace(import.meta.env.BASE_URL.replace(/\/$/, "") + path);
  }, [router, path]);
  return null;
}

const guestPages = { guest: "/sign-in", pending: "/mfa", enroll: "/enroll" } as const;

// A visitor in the wrong stage goes to the page of the stage they are in, remembering where they were going; a member
// who arrived on a guest page goes on to where they were going. Where they were going is the page the gate turned them
// away from, taken once: until the router has shown its first page, `here` is the address it is moving to, which this
// redirect itself changes, so reading it again would put that address in `next`, redirect again, and never stop.
function Redirect({ stage, here }: { stage: Stage; here: string }) {
  const next = useSearch({ strict: false }).next;
  const [from] = useState(here);
  if (stage === "member") {
    return <Replace path={next ?? "/"} />;
  }
  return <Replace path={`${guestPages[stage]}?next=${encodeURIComponent(next ?? from)}`} />;
}

// Gate shows its page only to a visitor in the stage the page is for; anyone else is sent where they belong. A member
// whose session ends while a page is open lands on the sign-in page and comes back to that page after signing in. Page
// state is the account's too: a session of another account starts the pages afresh.
export function Gate({ stage, children }: { stage: Stage; children: ReactNode }) {
  const { api, requiresMfa } = useAuth();
  const result = useQuery(sessionQuery(api));
  // A refresh that fails leaves the session the page already has in place: the server ends a session by saying so (401),
  // and a page open on something the user must act on, such as recovery codes, is not taken away for a bad moment.
  const session = result.data === undefined ? result : { ...result, status: "success" as const };
  // The page on screen, not the address the router is moving to: while the next page loads the current one stays, and
  // a gate that read the new address would redirect again from there.
  const here = useRouterState({ select: (state) => (state.resolvedLocation ?? state.location).href });
  return (
    <DataState query={session} page>
      {(data) =>
        stageOf(data, requiresMfa) === stage ? (
          <SessionContext key={identityOf(data)} value={data}>
            {children}
          </SessionContext>
        ) : (
          <Redirect stage={stageOf(data, requiresMfa)} here={here} />
        )
      }
    </DataState>
  );
}
