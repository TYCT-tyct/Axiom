import "./main.css";
import { consoleAuth } from "@axiom/api/auth";
import { AppProviders, preloadFirstPage } from "@axiom/app";
import { TanStackSessionNavigation } from "@axiom/app/tanstack";
import { crossDocuments, prepareArrival } from "@axiom/fx";
import { startApp } from "@axiom/ui";
import { RouterProvider } from "@tanstack/react-router";
import { initial as en } from "./locales/en";
import { initial as zh } from "./locales/zh";
import { PendingCodesProvider } from "./pending-codes";
import { router } from "./router";

crossDocuments();
prepareArrival();
preloadFirstPage(router);
startApp(
  "console",
  { zh, en },
  <AppProviders api={consoleAuth} requiresMfa={false} requiresConsent>
    <TanStackSessionNavigation router={router}>
      <PendingCodesProvider router={router}>
        <RouterProvider router={router} />
      </PendingCodesProvider>
    </TanStackSessionNavigation>
  </AppProviders>,
);
