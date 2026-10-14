import { type ReactNode, StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { type Messages, startI18n } from "./i18n";

// startApp is every app's entry: apply the chosen language before the first render, then render the app. The chosen
// theme is already applied, by public/theme.js.
export function startApp<T extends object>(ns: string, messages: { zh: T; en: Messages<T> }, app: ReactNode) {
  startI18n(ns, messages);
  const root = document.getElementById("root");
  if (!root) {
    throw new Error("index.html has no #root element");
  }
  createRoot(root).render(<StrictMode>{app}</StrictMode>);
}
