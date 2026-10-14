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
