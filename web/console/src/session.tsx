import { ApiError, consoleApi, type Schemas, setCsrfToken, unwrap, unwrapEmpty } from "@axiom/api";
import { DataState } from "@axiom/ui";
import { type QueryClient, queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Navigate, useRouter, useRouterState, useSearch } from "@tanstack/react-router";
import { createContext, type ReactNode, use, useEffect } from "react";

export type Session = Schemas["Session"];

// Where a visitor stands: nobody signed in (guest), a password proven and a second factor awaited (pending), or in
// (member). The server decides; the pages only route by it.
type Stage = "guest" | "pending" | "member";

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

async function fetchSession(signal: AbortSignal) {
  try {
    const session = await unwrap(consoleApi.GET("/api/console/v1/session", { signal }));
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
const sessionQuery = queryOptions({
  queryKey: sessionKey,
  queryFn: async ({ client, signal }) => {
    const before = client.getQueryData<Session | null>(sessionKey) ?? null;
    const session = await fetchSession(signal);
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

// endSession drops the session and what it loaded. Pages see the null session and send the visitor to sign in again,
// to come back to the page they were on.
export function endSession(queryClient: QueryClient) {
  setCsrfToken("");
  queryClient.setQueryData(sessionKey, null);
  queryClient.removeQueries({ queryKey: [accountKey] });
}

// startSession adopts the CSRF token of a new session and loads it.
export async function startSession(queryClient: QueryClient, csrfToken: string) {
  setCsrfToken(csrfToken);
  await queryClient.fetchQuery({ ...sessionQuery, staleTime: 0 });
}

// A changed factor keeps the account and gives this device a new token. The session is asked for again in the
// background, so that ask failing cannot take back an answer the change has already given (recovery codes are shown once).
export function adoptToken(queryClient: QueryClient, csrfToken: string) {
  setCsrfToken(csrfToken);
  void queryClient.invalidateQueries({ queryKey: sessionKey });
}

// The session starts from the response, which set the cookie and carries the CSRF token; whoever reads the session
// query then sees the gate move the visitor on, to the second step or to the page they came from.
export function useSignIn() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["SignInRequest"]) => unwrap(consoleApi.POST("/api/console/v1/auth/sign-in", { body })),
    onSuccess: (grant) => startSession(queryClient, grant.csrf_token),
  });
}

export function useSignOut() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => unwrapEmpty(consoleApi.POST("/api/console/v1/auth/sign-out")),
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

function stageOf(session: Session | null): Stage {
  if (!session) {
    return "guest";
  }
  return session.level === "pending_mfa" ? "pending" : "member";
}

// A path of this app, to return to after signing in. Anything else, such as another site's address, is dropped: the
// sign-in page must not be a way to send a visitor elsewhere, and a way back to a sign-in page would only loop.
export function safeNext(value: unknown) {
  const isGuestPage = /^\/(sign-in|mfa|register)(?![\w-])/;
  return typeof value === "string" && /^\/(?![/\\])/.test(value) && !isGuestPage.test(value) ? value : undefined;
}

// Back to where the visitor was going: a path inside the router, which the history takes with the app's base in front.
function Resume({ next }: { next: string }) {
  const router = useRouter();
  useEffect(() => {
    router.history.replace(import.meta.env.BASE_URL.replace(/\/$/, "") + next);
  }, [router, next]);
  return null;
}

function Redirect({ stage, here }: { stage: Stage; here: string }) {
  const next = useSearch({ strict: false }).next;
  if (stage === "member") {
    return <Resume next={next ?? "/"} />;
  }
  return <Navigate to={stage === "guest" ? "/sign-in" : "/mfa"} search={{ next: next ?? here }} replace />;
}

// Gate shows its page only to a visitor in the stage the page is for; anyone else is sent where they belong. A member
// whose session ends while a page is open lands on the sign-in page and comes back to that page after signing in. Page
// state is the account's too: a session of another account starts the pages afresh.
export function Gate({ stage, children }: { stage: Stage; children: ReactNode }) {
  const result = useQuery(sessionQuery);
  // A refresh that fails leaves the session the page already has in place: the server ends a session by saying so (401),
  // and a page open on something the user must act on, such as recovery codes, is not taken away for a bad moment.
  const session = result.data === undefined ? result : { ...result, status: "success" as const };
  // The page on screen, not the address the router is moving to: while the next page loads the current one stays, and
  // a gate that read the new address would redirect again from there.
  const here = useRouterState({ select: (state) => (state.resolvedLocation ?? state.location).href });
  return (
    <DataState query={session}>
      {(data) =>
        stageOf(data) === stage ? (
          <SessionContext key={identityOf(data)} value={data}>
            {children}
          </SessionContext>
        ) : (
          <Redirect stage={stageOf(data)} here={here} />
        )
      }
    </DataState>
  );
}
