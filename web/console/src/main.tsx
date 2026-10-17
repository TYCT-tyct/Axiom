import "./main.css";
import { consoleAuth } from "@axiom/api/auth";
import { AppProviders } from "@axiom/app";
import { startApp } from "@axiom/ui";
import { RouterProvider } from "@tanstack/react-router";
import en from "./locales/en";
import zh from "./locales/zh";
import { router } from "./router";

startApp(
  "console",
  { zh, en },
  <AppProviders api={consoleAuth} requiresMfa={false}>
    <RouterProvider router={router} />
  </AppProviders>,
);
