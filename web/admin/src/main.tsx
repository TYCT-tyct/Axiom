import "./main.css";
import { startApp } from "@axiom/ui";
import { RouterProvider } from "@tanstack/react-router";
import en from "./locales/en";
import zh from "./locales/zh";
import { router } from "./router";

startApp("admin", { zh, en }, <RouterProvider router={router} />);
