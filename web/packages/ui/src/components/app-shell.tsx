import { Slot } from "radix-ui";
import { type ComponentProps, type ReactNode, type RefObject, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { useBrandStyle } from "../brand";
import { cn } from "../cn";
import { Button } from "./button";
import { Icon } from "./icon";
import { Logo } from "./logo";
import { usePageTitle } from "./page-title";
import { ForbiddenHint, LoadingState, NotFoundState, PageFailedState } from "./states";

// AppShell is the frame of the console and the admin: a 216px sidebar beside the page, with a 48px top bar above the
// page for what header holds. Below 1024px the top bar always shows, with the logo and a menu button, and the sidebar
// moves into a drawer, a native modal <dialog>: it traps focus and closes on Escape by itself, with no
// script that injects styles, which the content security policy would block (docs/security.md「网页与域名」). banner
// holds a notice for the whole app above the top bar; it takes room from the page, never from the sidebar. forbiddenHint
// is the app's sentence for a person who lacks a permission, shown after the shared "no permission" text. brandColor
// is the site's #rrggbb, from which brandStyle derives both schemes' tokens (docs/frontend.md「主题与自由度」); without
// one the default palette applies.
export function AppShell({
  sidebar,
  header,
  banner,
  logo,
  forbiddenHint,
  brandColor,
  children,
}: {
  sidebar: ReactNode;
  header?: ReactNode;
  banner?: ReactNode;
  // What the compact top bar names the site with; the sidebar carries the same above the links.
  logo?: { name?: string; logo_url?: string };
  forbiddenHint?: string;
  brandColor?: string;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const drawer = useRef<HTMLDialogElement>(null);
  useBrandStyle(brandColor);
  // Paper gets the page alone: one column (with the sidebar gone, the grid would squeeze the page into its 216px) and
  // none of the app's notices, whose reload button is no part of a receipt (docs/frontend.md「打印」).
  return (
    <div className="min-h-dvh text-ui lg:grid lg:grid-cols-[216px_minmax(0,1fr)] print:block">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-btn focus:bg-s2 focus:px-3 focus:py-2"
      >
        {t("shell.skipToContent")}
      </a>
      {/* The frame stays off paper: a printed receipt is the page, not the navigation that led to it. The column itself
          takes the grid row's height, which is the page's when the page is longer than the window, so its edge and
          background reach the end of the document; the links sit in a viewport-tall box that stays in view. */}
      <nav aria-label={t("shell.navigation")} className="hidden border-r border-b1 bg-s1 lg:block print:hidden">
        <div className="sticky top-0 flex h-dvh flex-col gap-0.5 p-3">{sidebar}</div>
      </nav>
      <NavDrawer ref={drawer}>{sidebar}</NavDrawer>
      <div className="flex min-w-0 flex-col">
        {banner && <div className="print:hidden">{banner}</div>}
        <header
          className={cn(
            "sticky top-0 z-10 flex h-12 items-center gap-2 border-b border-b1 bg-bg px-4 print:hidden",
            !header && "lg:hidden",
          )}
        >
          <Button
            variant="ghost"
            size="icon"
            className="-ml-2 lg:hidden"
            aria-label={t("shell.openMenu")}
            onClick={() => drawer.current?.showModal()}
          >
            <Icon name="menu" />
          </Button>
          <Logo className="lg:hidden" name={logo?.name} logoUrl={logo?.logo_url} />
          {header}
        </header>
        <main id="main" tabIndex={-1} className="min-w-0 flex-1 outline-none">
          <ForbiddenHint value={forbiddenHint}>{children}</ForbiddenHint>
        </main>
      </div>
    </div>
  );
}

// NavDrawer holds the sidebar while the window is too narrow to show it beside the page. Once the window is wide
// enough, an open drawer would only cover the sidebar with the same links, so it closes.
function NavDrawer({ ref, children }: { ref: RefObject<HTMLDialogElement | null>; children: ReactNode }) {
  const { t } = useTranslation("common");
  useEffect(() => {
    const wide = window.matchMedia(`(min-width: ${desktopWidth}px)`);
    const close = () => {
      if (wide.matches) {
        ref.current?.close();
      }
    };
    wide.addEventListener("change", close);
    return () => wide.removeEventListener("change", close);
  }, [ref]);
  // Escape closes a modal dialog natively; the click handler only adds closing on the backdrop and on a link.
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard path is the dialog's own Escape and the close button
    <dialog
      ref={ref}
      aria-label={t("shell.navigation")}
      className="m-0 h-dvh max-h-none w-[min(280px,85vw)] border-r border-b1 bg-s1 p-3 text-ui text-t1 backdrop:bg-scrim"
      onClick={(event) => {
        // a click on the backdrop lands on the dialog itself; a followed link leaves the drawer behind
        if (event.target === ref.current || (event.target as Element).closest("a")) {
          ref.current?.close();
        }
      }}
    >
      <div className="flex h-full flex-col gap-0.5">
        <Button
          variant="ghost"
          size="icon"
          className="self-end"
          aria-label={t("shell.closeMenu")}
          onClick={() => ref.current?.close()}
        >
          <Icon name="close" />
        </Button>
        {children}
      </div>
    </dialog>
  );
}

// The width from which the sidebar shows beside the page: Tailwind's lg breakpoint, which the classes above use.
const desktopWidth = 1024;

// focusPage moves focus to the new page's heading after a navigation, so a screen reader announces the page instead of
// staying on the link or the menu button, and the next Tab starts from the page; the first load leaves focus where the
// browser puts it. Pass it to the router's onResolved event. The heading is made focusable here, for every page has
// one (docs/frontend.md「工程要求」); a page without one hands focus to #main. Focus does not scroll: the router has
// put the new page at its top. A drawer still open makes the page inert, where focus() does nothing, and closing it
// later hands focus back to the menu button: so it is closed first, whichever of the two events came first.
export function focusPage({ fromLocation, pathChanged }: { fromLocation?: unknown; pathChanged: boolean }) {
  if (fromLocation && pathChanged) {
    for (const dialog of document.querySelectorAll("dialog[open]")) {
      (dialog as HTMLDialogElement).close();
    }
    const heading = document.querySelector<HTMLElement>("#main h1");
    heading?.setAttribute("tabindex", "-1");
    (heading ?? document.getElementById("main"))?.focus({ preventScroll: true });
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

// SideNavItem styles the link it wraps as a sidebar entry; the router's link marks the current page with aria-current.
export function SideNavItem({ className, ...props }: ComponentProps<typeof Slot.Root>) {
  return (
    <Slot.Root
      className={cn(
        "flex h-[30px] items-center gap-2.5 rounded-sm px-2 text-t3 transition-colors duration-150 hover:bg-s2 hover:text-t1 aria-[current=page]:bg-s3 aria-[current=page]:text-t1",
        className,
      )}
      {...props}
    />
  );
}

export function PageHeader({ title, description }: { title: string; description?: string }) {
  usePageTitle(title);
  return (
    <div className="space-y-1 border-b border-b1 px-6 py-5">
      <h1 className="text-xl tracking-[-0.01em] text-t1">{title}</h1>
      {description ? <p className="text-ui text-t3">{description}</p> : null}
    </div>
  );
}
