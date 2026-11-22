// The console a sign-in leads to is a page load from the website's document (sign-in-host.tsx useSession). While the
// sign-in pages are up its code is asked for ahead, so that load reads it from the cache. The console's page names its
// code under names that change with it (cached for good); the page itself is never cached, so it is read here, not kept.
const code = 'script[type="module"][src], link[rel="modulepreload"][href], link[rel="stylesheet"][href]';
let asked = false;

export function askForConsole() {
  if (asked) return;
  asked = true;
  fetch("/console/", { credentials: "same-origin" })
    .then((page) => (page.ok ? page.text() : ""))
    .then((html) => {
      for (const named of new DOMParser().parseFromString(html, "text/html").querySelectorAll(code)) {
        const link = document.createElement("link");
        link.rel = "prefetch";
        link.href = named.getAttribute("src") ?? named.getAttribute("href") ?? "";
        document.head.append(link);
      }
    })
    // the console then loads as it would have: nothing was promised
    .catch(() => {});
}
