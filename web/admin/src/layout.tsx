import { can, Gate, NewVersionNotice, SignOutError, useSession, useSignOut } from "@axiom/app";
import WideNav from "@axiom/boards/AxApp/regions/AdminNav";
import Wide from "@axiom/boards/AxApp/regions/AdminShell";
import wideEn from "@axiom/boards/AxApp/regions/AdminShell.en";
import wideZh from "@axiom/boards/AxApp/regions/AdminShell.zh";
import PhoneMore from "@axiom/boards/AxAppPhone/regions/AdminMore";
import Phone from "@axiom/boards/AxAppPhone/regions/AdminShell";
import phoneEn from "@axiom/boards/AxAppPhone/regions/AdminShell.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/AdminShell.zh";
import PhoneTab from "@axiom/boards/AxAppPhone/regions/AdminTab";
import { BoardMark, BoardShell, useKeptPage, usePreferences } from "@axiom/ui";
import { Link, Outlet, useLocation, useMatchRoute } from "@tanstack/react-router";
import {
  type AnchorHTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
  type SyntheticEvent,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import { useAdminBrand } from "./brand";
import { nav, sheetIcons, tabIcons } from "./nav";

// The admin wears the console's shell (packages/boards/manifest/admin-shell.ts) until it has a design of its own: the
// AxApp board's bar and sidebar on wide windows and the AxAppPhone board's bar and tabs on phones, with the admin's
// pages in the navigation. As in the console, the document scrolls rather than the board's fixed frame, the window's
// width replaces the board's, and the admin's font stack the board's; the pages not ported yet bring their own padding.
const departures = {
  vars: "width: auto; font-family: var(--family-ui)",
  appStyle: "position: relative; inset: auto; min-height: 100dvh",
  barStyle: "position: sticky; top: 0; background: var(--page)",
  mainStyle: "overflow: visible; flex: 1 0 auto",
  pagesStyle: "padding: 0; max-width: none",
  bodyStyle: "flex: 1 0 auto",
  // 56px is the bar's height.
  sidebarStyle: "position: sticky; top: 56px; height: calc(100dvh - 56px)",
  panelStyle: "overflow: clip",
  sheetStyle: "position: fixed",
  // The command palette is not built; the bar keeps its place.
  placeholder: "visibility: hidden",
  monoStyle: "font-family: var(--family-mono)",
};
const frameStyle = "overflow: clip; min-height: 100dvh";
// The board's state classes for the personal space, which is the level whose sidebar the admin's navigation replaces.
const rootState = "lvP pg-none spP woP wa2 rw tx0 tm1 ap0 lf0 wsk0 nks0 cz0 ck0 ct1 den0 kp0 yrc0 pico0";

// An entry the board draws without an address, given a button's role, is pressed with Enter or Space too.
function press(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  (event.currentTarget as HTMLElement).click();
}

// The wide sidebar shows from 1024px; below it the board's tablet layout folds it, and below 820px the phone board
// takes over. Only one board is in the DOM.
function useWidth(query: string) {
  return useSyncExternalStore(
    (changed) => {
      const list = matchMedia(query);
      list.addEventListener("change", changed);
      return () => list.removeEventListener("change", changed);
    },
    () => matchMedia(query).matches,
  );
}

// The router's link where the board draws an <a>, which marks the current page with aria-current.
function RouterLink({ href, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) {
  return <Link to={href} activeOptions={{ exact: href === "/" }} {...props} />;
}

type Menu = "me" | "more";

// The account menu and the phone's sheet of more pages, one open at a time and only on the page it was opened on. A
// press outside it and its button, or Escape, closes it; Escape also gives focus back to its button.
function useMenus() {
  const pathname = useLocation({ select: (location) => location.pathname });
  const [state, setState] = useState<{ menu: Menu; on: string }>();
  const button = useRef<HTMLElement>(null);
  const open = state?.on === pathname ? state.menu : undefined;
  useEffect(() => {
    if (!open) return;
    const listening = new AbortController();
    const options = { signal: listening.signal };
    document.addEventListener(
      "pointerdown",
      ({ target }) => {
        if (!(target as Element).closest('[data-shell="popover"], [aria-expanded]')) setState(undefined);
      },
      options,
    );
    document.addEventListener(
      "keydown",
      ({ key }) => {
        if (key !== "Escape") return;
        setState(undefined);
        button.current?.focus();
      },
      options,
    );
    return () => listening.abort();
  }, [open]);
  const toggle = (menu: Menu) => (event: SyntheticEvent) => {
    button.current = event.currentTarget as HTMLElement;
    setState(open === menu ? undefined : { menu, on: pathname });
  };
  return {
    pop_me: open === "me" ? "open" : "",
    pop_more: open === "more" ? "open" : "",
    meOpen: open === "me",
    moreOpen: open === "more",
    oMe: toggle("me"),
    oMore: toggle("more"),
  };
}

// The admin's pages the session may open, each with its label, its borrowed icon and whether it is open now.
function usePages() {
  const { t } = useTranslation();
  const session = useSession();
  const matchRoute = useMatchRoute();
  return nav
    .filter((page) => can(session, page.permission))
    .map((page) => ({
      ...page,
      label: t(page.label),
      current: Boolean(matchRoute({ to: page.to, fuzzy: page.to !== "/" })),
    }));
}
type Page = ReturnType<typeof usePages>[number];

// Which of the board's entries an entry region, which draws them all, shows: the one with the icon, or none.
const only = (icons: readonly string[], shown?: string) =>
  Object.fromEntries(icons.map((icon) => [`not_${icon}`, icon !== shown]));

// What an entry region needs to show one page: its address, whether it is current, and the entry with its icon.
function entry(page: Page, current: string, icons: readonly string[]) {
  return { to: page.to, on: page.current ? current : "", not_more: true, ...only(icons, page.icon) };
}
const wideIcons = [...tabIcons, ...sheetIcons];

export function Layout() {
  const { t } = useTranslation();
  const signOut = useSignOut();
  return (
    <Gate stage="member">
      <BoardShell
        banner={
          <>
            <NewVersionNotice />
            <SignOutError error={signOut.error} />
          </>
        }
        forbiddenHint={t("forbiddenHint")}
      >
        <AdminShell signOut={() => signOut.isPending || signOut.mutate()}>
          <Outlet />
        </AdminShell>
      </BoardShell>
    </Gate>
  );
}

function AdminShell({ signOut, children }: { signOut: () => void; children: ReactNode }) {
  const { t, i18n } = useTranslation();
  const { page, slot } = useKeptPage(children);
  const brand = useAdminBrand();
  const session = useSession();
  const preferences = usePreferences();
  const phone = useWidth("(max-width: 819px)");
  const wide = useWidth("(min-width: 1024px)");
  const [folded, setFolded] = useState<boolean>();
  const sidebarFolded = folded ?? !wide;
  const pages = usePages();
  const theme = preferences.themes.indexOf(preferences.theme);
  const email = session.user.email;
  const values = {
    ...departures,
    ...useMenus(),
    press,
    rootC: `${rootState}${preferences.dark ? " thD" : ""}`,
    to_ov: "/",
    home: brand.name,
    navigation: t("shell.navigation", { ns: "common" }),
    meN: email.split("@")[0],
    meE: email,
    meI: email.slice(0, 1).toUpperCase(),
    meC: 0,
    go_prefs: preferences.other.choose,
    languageTag: preferences.other.locale,
    tsi: theme,
    // The switch's buttons in the order of the themes: chosen, pressed with a click, and pressed for screen readers.
    ...Object.fromEntries(
      preferences.themes.flatMap((name, i) => [
        [`tsg${i}`, i === theme ? "on" : ""],
        [`th${i}`, () => preferences.chooseTheme(name)],
        [`theme${name.charAt(0).toUpperCase()}${name.slice(1)}`, i === theme],
      ]),
    ),
    fnOut: signOut,
    cold: sidebarFolded ? "cold" : "",
    tgCol: () => setFolded(!sidebarFolded),
    sidebarOpen: !sidebarFolded,
    colT: t(sidebarFolded ? "shell.expand" : "shell.collapse"),
  };
  const text = { language: preferences.other.name, lang: preferences.locale, frameStyle };
  const words = (zh: Record<string, string>, en: Record<string, string>) => (key: string) =>
    (i18n.language === "en" ? en : zh)[key] as string;
  const mark = <BoardMark name={brand.name} logoUrl={brand.logo_url} />;
  const tabs = pages.filter((p) => (tabIcons as readonly string[]).includes(p.icon));
  const more = pages.filter((p) => !tabs.includes(p));
  // The page comes after the board, so the slot holds the page's element before the page's own effects run.
  return (
    <>
      {phone ? (
        <Phone
          t={words(phoneZh, phoneEn)}
          values={{
            ...values,
            tabsStyle: `position: sticky; bottom: 0; grid-template-columns: repeat(${tabs.length + (more.length ? 1 : 0)}, 1fr)`,
          }}
          text={text}
          link={RouterLink}
          mark={mark}
          tabs={
            <>
              {tabs.map((p) => (
                <PhoneTab key={p.to} t={String} values={entry(p, "on", tabIcons)} link={RouterLink} label={p.label} />
              ))}
              {more.length ? (
                <PhoneTab
                  t={String}
                  values={{
                    ...only(tabIcons),
                    moreC: values.moreOpen ? "on" : "",
                    oMore: values.oMore,
                    moreOpen: values.moreOpen,
                    press,
                  }}
                  label={t("nav.more")}
                />
              ) : null}
            </>
          }
          more={more.map((p) => (
            <PhoneMore key={p.to} t={String} values={entry(p, "on", sheetIcons)} link={RouterLink} label={p.label} />
          ))}
        >
          {slot}
        </Phone>
      ) : (
        <Wide
          t={words(wideZh, wideEn)}
          values={values}
          text={text}
          link={RouterLink}
          mark={mark}
          nav={pages.map((p) => (
            <WideNav key={p.to} t={String} values={entry(p, "on", wideIcons)} link={RouterLink} label={p.label} />
          ))}
        >
          {slot}
        </Wide>
      )}
      {page}
    </>
  );
}
