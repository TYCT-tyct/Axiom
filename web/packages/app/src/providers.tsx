import { isTransient } from "@axiom/api";
import type { AuthApi } from "@axiom/api/auth";
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { AuthProvider, endUnauthenticated } from "./session";
import { RecoveryCodesHost } from "./totp-dialogs";

// A call refused for want of a session means the session has ended (it timed out, or another device ended it): whatever
// the page was doing, it goes to the sign-in page and returns here after.
function createQueryClient() {
  const endOnUnauthenticated = (error: unknown): void => endUnauthenticated(queryClient, error);
  const queryClient: QueryClient = new QueryClient({
    queryCache: new QueryCache({ onError: endOnUnauthenticated }),
    mutationCache: new MutationCache({ onError: endOnUnauthenticated }),
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        retry: (failures, error) => failures < 2 && isTransient(error),
      },
    },
  });
  return queryClient;
}

// AppProviders is what both apps stand on: the query cache, which treats a call refused for want of a session as the
// end of the session, and the surface's sign-in calls.
export function AppProviders({
  api,
  requiresMfa,
  children,
}: {
  api: AuthApi;
  requiresMfa: boolean;
  children: ReactNode;
}) {
  const [queryClient] = useState(createQueryClient);
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider api={api} requiresMfa={requiresMfa}>
        <RecoveryCodesHost>{children}</RecoveryCodesHost>
      </AuthProvider>
    </QueryClientProvider>
  );
}
