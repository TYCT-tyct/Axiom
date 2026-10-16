import "./main.css";
import { adminAuth } from "@axiom/api/auth";
import { AppProviders } from "@axiom/app";
import { startApp } from "@axiom/ui";
import { RouterProvider } from "@tanstack/react-router";
import en from "./locales/en";
import zh from "./locales/zh";
import { router } from "./router";

startApp(
  "admin",
  { zh, en },
  <AppProviders api={adminAuth} requiresMfa>
    <RouterProvider router={router} />
  </AppProviders>,
);
