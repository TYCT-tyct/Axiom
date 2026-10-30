import { Slot } from "radix-ui";
import { type ComponentProps, type ReactNode, type RefObject, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
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
// is the app's sentence for a person who lacks a permission, shown after the shared "no permission" text.
export function AppShell({
  sidebar,
  header,
  banner,
  forbiddenHint,
  children,
}: {
  sidebar: ReactNode;
  header?: ReactNode;
  banner?: ReactNode;
  forbiddenHint?: string;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  const drawer = useRef<HTMLDialogElement>(null);
  return (
    <div className="min-h-dvh text-ui lg:grid lg:grid-cols-[216px_minmax(0,1fr)]">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-btn focus:bg-s2 focus:px-3 focus:py-2"
      >
        {t("shell.skipToContent")}
      </a>
      <nav
        aria-label={t("shell.navigation")}
        className="sticky top-0 hidden h-dvh flex-col gap-0.5 border-r border-b1 bg-s1 p-3 lg:flex"
      >
        {sidebar}
      </nav>
      <NavDrawer ref={drawer}>{sidebar}</NavDrawer>
      <div className="flex min-w-0 flex-col">
        {banner}
        <header
          className={cn(
            "sticky top-0 z-10 flex h-12 items-center gap-2 border-b border-b1 bg-bg px-4",
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
          <Logo className="lg:hidden" />
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

// routerDefaults are the options every app's router starts from. A link's page loads as soon as the pointer or focus
// is on the link; while the next page loads the current one stays, and past a short wait the layout stays and the page
// area shows a skeleton, so navigating never blanks the screen.
export const routerDefaults = {
  defaultPreload: "intent",
  defaultPendingMs: 150,
  defaultPendingComponent: LoadingState,
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
