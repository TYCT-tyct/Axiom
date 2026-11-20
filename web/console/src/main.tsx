import "./main.css";
import { consoleAuth } from "@axiom/api/auth";
import { AppProviders, preloadFirstPage } from "@axiom/app";
import { startApp } from "@axiom/ui";
import { RouterProvider } from "@tanstack/react-router";
import en from "./locales/en";
import zh from "./locales/zh";
import { PendingCodesProvider } from "./pending-codes";
import { router } from "./router";

preloadFirstPage(router);
startApp(
  "console",
  { zh, en },
  <AppProviders api={consoleAuth} requiresMfa={false} requiresConsent>
    <PendingCodesProvider router={router}>
      <RouterProvider router={router} />
    </PendingCodesProvider>
  </AppProviders>,
);
