// Applies the saved theme before the first paint, so a page whose user chose another scheme than the system's does not
// flash the system's first. A classic script loaded from the page's own origin, because the content security policy
// forbids inline ones; the key and the values are the ones packages/ui/src/preferences.ts keeps.
try {
  const theme = localStorage.getItem("axiom.theme");
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
  }
} catch {
  // storage is blocked: the page follows the system
}

// The first Intl format a page builds also loads the engine's number, calendar and zone data: about 30 ms on a fast
// core and over 100 ms of a phone's main thread at the budget's 4x CPU, which the console's first render paid before
// it could paint (#747). While the app's code downloads, the main thread waits, so one of each kind is built then; the
// data stays loaded for the formats the page builds later. In a task of its own, so the parser does not wait for it.
setTimeout(() => {
  new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(1);
  new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).format(0);
});
