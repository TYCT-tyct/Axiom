import { ApiError, isTransient } from "@axiom/api";
import type { AuthApi } from "@axiom/api/auth";
import {
  type DefaultError,
  MutationCache,
  type Query,
  QueryCache,
  QueryClient,
  QueryClientProvider,
  type QueryKey,
  type QueryOptions,
  type QueryState,
  type WithRequired,
} from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { AuthProvider, currentSession, endUnauthenticated, sessionBinding } from "./session";
import { RecoveryCodesHost } from "./totp-dialogs";

// A call refused for want of a session means the session that made it has ended (it timed out, or another device ended
// it): whatever the page was doing, it goes to the sign-in page and returns here after. The browser's session is shared
// by every tab, though, so an answer belongs to the session that started the call, not to whoever is signed in when it
// lands; a late refusal from before another tab signed in must not end the session that replaced it.

// SessionBoundQueryCache remembers the session each fetch belongs to in that fetch's own meta. QueryCache has no
// per-fetch hook, so it wraps Query.fetch when the cache builds a query. A duplicate fetch that reuses the request in
// flight returns before it reads the new meta, so the binding kept is the one of the request that actually runs.
class SessionBoundQueryCache extends QueryCache {
  readonly #clients = new WeakMap<object, QueryClient>();
  readonly #startedIn = new WeakMap<object, string>();
  readonly #bound = new WeakSet<object>();

  constructor() {
    super({
      onError: (error, query) => {
        const client = this.#clients.get(query);
        const meta = query.state.fetchMeta;
        if (client && meta) {
          endUnauthenticated(client, error, this.#startedIn.get(meta) ?? "");
        }
      },
    });
  }

  override build<
    TQueryFnData = unknown,
    TError = DefaultError,
    TData = TQueryFnData,
    TQueryKey extends QueryKey = QueryKey,
  >(
    client: QueryClient,
    options: WithRequired<QueryOptions<TQueryFnData, TError, TData, TQueryKey>, "queryKey">,
    state?: QueryState<TData, TError>,
  ): Query<TQueryFnData, TError, TData, TQueryKey> {
    const query = super.build(client, options, state);
    this.#clients.set(query, client);
    if (!this.#bound.has(query)) {
      this.#bound.add(query);
      const fetch = query.fetch.bind(query);
      query.fetch = (options, fetchOptions) => {
        const meta = { ...(fetchOptions?.meta ?? {}) };
        this.#startedIn.set(meta, sessionBinding(currentSession(client)));
        return fetch(options, { ...fetchOptions, meta });
      };
    }
    return query;
  }
}

export function createQueryClient() {
  // A mutation's binding is captured before its function runs, so a request still out from the session before another
  // tab signed in cannot end the one that replaced it.
  const startedIn = new WeakMap<object, string>();
  const queryClient: QueryClient = new QueryClient({
    queryCache: new SessionBoundQueryCache(),
    mutationCache: new MutationCache({
      onMutate: (_variables, mutation) => {
        startedIn.set(mutation, sessionBinding(currentSession(queryClient)));
      },
      onError: (error, _variables, _context, mutation) => {
        endUnauthenticated(queryClient, error, startedIn.get(mutation) ?? "");
      },
    }),
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        retry: (failures, error) => failures < 2 && isTransient(error),
        // A refusal (403, or 401 for want of a permission) answers the same until the grant changes, so a page that
        // mounts after it reads the refusal instead of asking again; any other failure is tried again on mount.
        retryOnMount: (query) =>
          !(query.state.error instanceof ApiError && query.state.error.status < 500 && query.state.error.status > 0),
      },
    },
  });
  return queryClient;
}

// AppProviders is what both apps stand on: the query client, which treats a call refused for want of the session that
// made it as the end of that session, and the surface's sign-in calls.
export function AppProviders({
  api,
  requiresMfa,
  requiresConsent = false,
  children,
}: {
  api: AuthApi;
  requiresMfa: boolean;
  requiresConsent?: boolean;
  children: ReactNode;
}) {
  const [queryClient] = useState(createQueryClient);
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider api={api} requiresMfa={requiresMfa} requiresConsent={requiresConsent}>
        <RecoveryCodesHost>{children}</RecoveryCodesHost>
      </AuthProvider>
    </QueryClientProvider>
  );
}
