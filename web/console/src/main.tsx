import "./main.css";
import { startApp } from "@axiom/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import en from "./locales/en";
import zh from "./locales/zh";
import { router } from "./router";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      // A refused request (4xx) fails the same way again; only a lost response or a server error is retried.
      retry: (failures, error) =>
        failures < 2 && (!("status" in error) || error.status === 0 || Number(error.status) >= 500),
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
