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

// The current session, or null when nobody is signed in. A 401 is the answer "no session", not a failure, and the page
// keeps the CSRF token it carries in memory (@axiom/api) because the cookie itself is HttpOnly.
const sessionQuery = queryOptions({
  queryKey: sessionKey,
  queryFn: async ({ signal }) => {
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
  },
  refetchOnWindowFocus: "always",
});

// Everything under this key belongs to one account's session and goes when it ends, so the next person to sign in on
// this browser never sees it.
export const accountKey = "account";

// endSession drops the session and what it loaded. Pages see the null session and send the visitor to sign in again,
// to come back to the page they were on.
export function endSession(queryClient: QueryClient) {
  setCsrfToken("");
  queryClient.setQueryData(sessionKey, null);
  queryClient.removeQueries({ queryKey: [accountKey] });
}

// startSession adopts the CSRF token of a new session and loads it; the previous account's data is dropped first.
export async function startSession(queryClient: QueryClient, csrfToken: string) {
  setCsrfToken(csrfToken);
  queryClient.removeQueries({ queryKey: [accountKey] });
  await queryClient.fetchQuery({ ...sessionQuery, staleTime: 0 });
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
// whose session ends while a page is open lands on the sign-in page and comes back to that page after signing in.
export function Gate({ stage, children }: { stage: Stage; children: ReactNode }) {
  const session = useQuery(sessionQuery);
  // The page on screen, not the address the router is moving to: while the next page loads the current one stays, and
  // a gate that read the new address would redirect again from there.
  const here = useRouterState({ select: (state) => (state.resolvedLocation ?? state.location).href });
  return (
    <DataState query={session}>
      {(data) =>
        stageOf(data) === stage ? (
          <SessionContext value={data}>{children}</SessionContext>
        ) : (
          <Redirect stage={stageOf(data)} here={here} />
        )
      }
    </DataState>
  );
}
