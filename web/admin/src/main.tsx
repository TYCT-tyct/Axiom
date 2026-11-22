import "./main.css";
import { adminAuth } from "@axiom/api/auth";
import { AppProviders, preloadFirstPage } from "@axiom/app";
import { TanStackSessionNavigation } from "@axiom/app/tanstack";
import { startApp } from "@axiom/ui";
import { RouterProvider } from "@tanstack/react-router";
import { initial as en } from "./locales/en";
import { initial as zh } from "./locales/zh";
import { router } from "./router";

preloadFirstPage(router);
startApp(
  "admin",
  { zh, en },
  <AppProviders api={adminAuth} requiresMfa>
    <TanStackSessionNavigation router={router}>
      <RouterProvider router={router} />
    </TanStackSessionNavigation>
  </AppProviders>,
);
