import type { Page } from "@playwright/test";

// The errors the page logs to its console or throws, collected as they happen.
export function collectErrors(page: Page) {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      errors.push(message.text());
    }
  });
  page.on("pageerror", (error) => errors.push(error.message));
  return errors;
}

// Some private modes, and blocked site data, make even reading the storage throw.
export function blockStorage(page: Page, storage: "localStorage" | "sessionStorage") {
  return page.addInitScript((name) => {
    Object.defineProperty(window, name, {
      get: () => {
        throw new DOMException("The site's data is blocked", "SecurityError");
      },
    });
  }, storage);
}
