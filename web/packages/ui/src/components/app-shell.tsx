import { usePageTitle } from "./page-title";
import { LoadingState, NotFoundState, PageFailedState } from "./states";

// focusPage moves focus to the page after a navigation, so a screen reader announces the new page instead of staying
// on the link or the menu button; the first load leaves focus where the browser puts it. Pass it to the router's
// onResolved event. A drawer still open makes the page inert, where focus() does nothing, and closing it later hands
// focus back to the menu button: so it is closed first, whichever of the two events came first.
export function focusPage({ fromLocation, pathChanged }: { fromLocation?: unknown; pathChanged: boolean }) {
  if (fromLocation && pathChanged) {
    for (const dialog of document.querySelectorAll("dialog[open]")) {
      (dialog as HTMLDialogElement).close();
    }
    document.getElementById("main")?.focus();
  }
}

// The router's pending state stands in for the page area while the page's chunk loads, so it carries a level-one
// heading too (docs/frontend.md「工程要求」): a page whose heading is missing while it waits is one a reader, and a
// check, cannot tell apart from a page that never rendered (#363).
const PageLoading = () => <LoadingState page />;

// routerDefaults are the options every app's router starts from. A link's page loads as soon as the pointer or focus
// is on the link; while the next page loads the current one stays, and past a short wait the layout stays and the page
// area shows a skeleton, so navigating never blanks the screen.
export const routerDefaults = {
  defaultPreload: "intent",
  defaultPendingMs: 150,
  defaultPendingComponent: PageLoading,
  defaultErrorComponent: PageFailedState,
  defaultNotFoundComponent: NotFoundState,
} as const;

export function PageHeader({ title, description }: { title: string; description?: string }) {
  usePageTitle(title);
  return (
    <div className="space-y-1 border-b border-b1 px-6 py-5">
      <h1 className="text-xl tracking-[-0.01em] text-t1">{title}</h1>
      {description ? <p className="text-ui text-t3">{description}</p> : null}
    </div>
  );
}
