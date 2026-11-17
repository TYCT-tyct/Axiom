import { type ReactNode, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useBrandStyle } from "../brand";
import { chooseLanguage, type Language, languageNames, languages, locales } from "../i18n";
import { chooseTheme, currentTheme, subscribeTheme, themes } from "../preferences";
import styles from "./board-shell.module.css";
import { ForbiddenHint } from "./states";

// BoardShell frames a shell a design board draws (#747) as a page of the app: a skip link to #main, the app's notices
// floating above it, and the rules the board's fixed frame lacks (board-shell.module.css). forbiddenHint and brandColor
// are AppShell's.
export function BoardShell({
  banner,
  forbiddenHint,
  brandColor,
  children,
}: {
  banner?: ReactNode;
  forbiddenHint?: string;
  brandColor?: string;
  children: ReactNode;
}) {
  const { t } = useTranslation("common");
  useBrandStyle(brandColor);
  return (
    <div className={styles.root}>
      <a href="#main" className={styles.skip}>
        {t("shell.skipToContent")}
      </a>
      {banner && <div className={styles.banner}>{banner}</div>}
      <ForbiddenHint value={forbiddenHint}>{children}</ForbiddenHint>
    </div>
  );
}

// useKeptPage keeps a page mounted while the shell around it changes from one board's to another's, as the window
// crosses a board's width: the page renders into an element of its own at one place in the tree, and the slot of
// whichever board is shown takes that element in. Rendered inside the boards' slots instead, the page would be
// mounted again with the board, losing what it holds, such as a new key's secret shown only once.
export function useKeptPage(page: ReactNode) {
  const [{ host, take }] = useState(() => {
    const element = document.createElement("div");
    element.className = styles.page as string;
    // One callback for the hook's life: a new one each render would make React detach and attach the slot's ref, and
    // moving the page blurs whatever has focus in it, such as the heading a navigation focused.
    const attach = (slot: HTMLDivElement | null) => {
      if (!slot || element.parentNode === slot) return;
      slot.append(element);
      // Moving the page took its open modal dialogs out of the top layer, leaving them open but not modal. Showing
      // them again restores that; dropping the open attribute first fires no close event, which the page would take
      // for the person dismissing it.
      for (const dialog of element.querySelectorAll<HTMLDialogElement>("dialog[open]")) {
        if (!dialog.matches(":modal")) {
          dialog.removeAttribute("open");
          dialog.showModal();
        }
      }
    };
    return { host: element, take: attach };
  });
  return { page: createPortal(page, host), slot: <div className={styles.page} ref={take} /> };
}

// BoardMark is the site's logo in a board's mark, in a 20px box whatever its size (docs/frontend.md「主题与自由度」);
// without one, or when it does not load, the initial of the site's name, as the boards draw Axiom's A.
export function BoardMark({ name, logoUrl }: { name?: string; logoUrl?: string }) {
  const { t } = useTranslation("common");
  // The URL that failed, not a boolean: a new logo at a new URL is tried, not hidden behind the old failure.
  const [failed, setFailed] = useState<string | null>(null);
  return logoUrl && failed !== logoUrl ? (
    <img src={logoUrl} alt="" width="20" height="20" onError={() => setFailed(logoUrl)} className={styles.logo} />
  ) : (
    (name ?? t("brand")).slice(0, 1).toUpperCase()
  );
}

// focusHeading moves focus to the new page's heading after a navigation, so a screen reader announces the page instead of
// staying on the link or the menu, and the next Tab starts from the page; the first load leaves focus where the browser
// puts it. Pass it to the router's onResolved event. The heading is made focusable here, for every page has one
// (docs/frontend.md「工程要求」); a page without one hands focus to #main. Focus does not scroll: the router has put the
// new page at its top.
export function focusHeading({ fromLocation, pathChanged }: { fromLocation?: unknown; pathChanged: boolean }) {
  if (fromLocation && pathChanged) {
    const heading = document.querySelector<HTMLElement>("#main h1");
    heading?.setAttribute("tabindex", "-1");
    (heading ?? document.getElementById("main"))?.focus({ preventScroll: true });
  }
}

const darkScheme = () => matchMedia("(prefers-color-scheme: dark)");
const subscribeScheme = (listener: () => void) => {
  const scheme = darkScheme();
  scheme.addEventListener("change", listener);
  return () => scheme.removeEventListener("change", listener);
};

// usePreferences gives a board's switches the page's own theme and language, so the shell shows what the page has:
// dark is the scheme in effect, the system's when the theme follows it; other is the language a switch changes to.
export function usePreferences() {
  const { i18n } = useTranslation();
  const theme = useSyncExternalStore(subscribeTheme, currentTheme);
  const systemDark = useSyncExternalStore(subscribeScheme, () => darkScheme().matches);
  const language = i18n.language as Language;
  const other = languages.find((l) => l !== language) as Language;
  return {
    theme,
    themes,
    chooseTheme,
    dark: theme === "dark" || (theme === "system" && systemDark),
    locale: locales[language],
    other: { name: languageNames[other], locale: locales[other], choose: () => chooseLanguage(other) },
  };
}
