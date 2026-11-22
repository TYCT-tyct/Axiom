import { currentSession } from "@axiom/app";
import type { QueryClient } from "@tanstack/react-query";

type Session = NonNullable<ReturnType<typeof currentSession>>;

// The router's context: the query cache the pages read, which a route's loader fills ahead of its page.
export type RouterContext = { queryClient: QueryClient };

// What a page gives its route's loader: the queries its first render reads, for the session the cache holds and the
// route's search (loaderDeps). They are the page's own options, so the page mounts on the answers rather than asking.
export type Warm<D = unknown> = (client: QueryClient, session: Session, deps: D) => Promise<unknown>;

// A query a page reads, as its hook passes it: an infinite list carries how to page on (getNextPageParam), and one the
// page does not ask for (enabled false) is not asked for ahead of it either.
type Asked = { queryKey: readonly unknown[]; enabled?: unknown; getNextPageParam?: unknown };

// ask asks for the queries a page reads that it would ask for itself.
export function ask(client: QueryClient, queries: Asked[]) {
  return Promise.all(
    queries
      .filter((query) => query.enabled !== false)
      .map((query) =>
        "getNextPageParam" in query
          ? client.prefetchInfiniteQuery(query as Parameters<QueryClient["prefetchInfiniteQuery"]>[0])
          : client.prefetchQuery(query as Parameters<QueryClient["prefetchQuery"]>[0]),
      ),
  );
}

// How long a navigation waits for a page's data before showing the page anyway: a slower answer is the page's own to
// show as loading, so one slow request never holds a page.
const warmFor = 400;

// warming makes a route's loader from its page's warm, which may come in a chunk of its own: on a press's intent
// (defaultPreload) and on the way in, the old page staying up while the answers come. Only once the session is known:
// the first answer for it drops what was asked for the account before it (session.tsx), and the page's permissions are
// the session's. Data that cannot come leaves the page to ask for it itself.
export function warming<D>(load: () => Promise<Warm<D>>) {
  return ({ context, deps }: { context: RouterContext; deps: D }) => {
    const session = currentSession(context.queryClient);
    if (!session) return;
    return Promise.race([
      load()
        .then((warm) => warm(context.queryClient, session, deps))
        .catch(() => undefined),
      new Promise((done) => window.setTimeout(done, warmFor)),
    ]);
  };
}
