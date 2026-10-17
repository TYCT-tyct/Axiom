import "./main.css";
import { ApiError, isTransient } from "@axiom/api";
import { startApp } from "@axiom/ui";
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import en from "./locales/en";
import zh from "./locales/zh";
import { router } from "./router";
import { endSession } from "./session";

// A call refused for want of a session means the session has ended (it timed out, or another device ended it): whatever
// the page was doing, it goes to the sign-in page and returns here after.
function endOnUnauthenticated(error: unknown) {
  if (error instanceof ApiError && error.code === "unauthenticated") {
    endSession(queryClient);
  }
}

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: endOnUnauthenticated }),
  mutationCache: new MutationCache({ onError: endOnUnauthenticated }),
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: (failures, error) => failures < 2 && isTransient(error),
    },
  },
});

startApp(
  "console",
  { zh, en },
  <QueryClientProvider client={queryClient}>
    <RouterProvider router={router} />
  </QueryClientProvider>,
);
