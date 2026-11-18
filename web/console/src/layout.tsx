import { can, Gate, NewVersionNotice, SignOutError, useSession, useSignOut } from "@axiom/app";
import Wide from "@axiom/boards/AxApp/regions/Shell";
import wideEn from "@axiom/boards/AxApp/regions/Shell.en";
import wideZh from "@axiom/boards/AxApp/regions/Shell.zh";
import Phone from "@axiom/boards/AxAppPhone/regions/Shell";
import phoneEn from "@axiom/boards/AxAppPhone/regions/Shell.en";
import phoneZh from "@axiom/boards/AxAppPhone/regions/Shell.zh";
import { BoardMark, BoardShell, useFormat, useKeptPage, usePreferences } from "@axiom/ui";
import { useQuery } from "@tanstack/react-query";
import { Link, type LinkProps, Outlet, useLocation, useMatchRoute, useNavigate } from "@tanstack/react-router";
import {
  type AnchorHTMLAttributes,
  type KeyboardEvent as KeyEvent,
  type ReactNode,
  type SyntheticEvent,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import { balanceQuery, siteQuery } from "./queries";

// The pages the shell links to, by the board's page key: the address, what the session must hold to open it, and the
// other addresses the same entry stands for. The board's other entries are not built yet (#816); the workspaces page is
// reached from the settings page, as the board's workspace level needs team spaces.
const pages = {
  ov: { to: "/" },
  logs: { to: "/calls", permission: "calls.view" },
  keys: { to: "/keys", permission: "api_keys.view" },
  presets: { to: "/request-presets", permission: "api_keys.view" },
  route: { to: "/routing-presets", permission: "api_keys.view" },
  byok: { to: "/provider-keys", permission: "api_keys.view" },
  bill: { to: "/balance", permission: "balance.view", under: ["/redeem", "/orders", "/statement"] },
  profile: { to: "/settings", under: ["/workspaces"] },
  security: { to: "/security" },
  notify: { to: "/notifications" },
  models: { to: "/models" },
} satisfies Record<string, { to: LinkProps["to"] & string; permission?: string; under?: string[] }>;
type Page = keyof typeof pages;
const keys = Object.keys(pages) as Page[];
// On these the sidebar shows the account's entries, with a way back, as the board's account level does.
const account: Page[] = ["profile", "security", "notify"];

// The board's own state classes for the personal space, as it draws it; only the level and the page vary here.
const drawn = "spP woP wa2 rw tx0 tm1 ap0 lf0 wsk0 nks0 cz0 ck0 ct1 den0 kp0 yrc0 pico0";

// The widths the boards are drawn for: the wide board from the tablet board's 820px (AxAppTablet, the same markup with
// its sidebar folded, as it stays below 1024px), the phone board below, where the page keeps the whole width. Only one
// of the two is in the DOM.
const media = (query: string) => ({
  subscribe: (listener: () => void) => {
    const list = matchMedia(query);
    list.addEventListener("change", listener);
    return () => list.removeEventListener("change", listener);
  },
  matches: () => matchMedia(query).matches,
});
const phoneWidth = media("(max-width: 819px)");
const wideWidth = media("(min-width: 1024px)");
const useMedia = (query: ReturnType<typeof media>) => useSyncExternalStore(query.subscribe, query.matches);

// The shell's departures from the drawing, which the regions leave to the page (packages/boards/manifest/shell.ts). The document
// scrolls, not the board's fixed frame (port design §3.3): the bar sticks to the top, the sidebar below it (56px is the
// bar), the phone's tabs to the bottom, and the phone's popovers are sheets at the bottom of the window. The board's
// width gives way to the window's, and the console's font stacks, whose Chinese faces are cut for its pages, replace
// the drawing's; tokens.css carries the colours. The pages not ported yet bring their own padding. Set inline, these
// outrank the board's rules. What is not built (#816) is hidden (board-shell.module.css), and the bar keeps the places
// of its unbuilt buttons.
const frame = {
  vars: "width: auto; font-family: var(--family-ui)",
  appStyle: "position: relative; inset: auto; min-height: 100dvh",
  barStyle: "position: sticky; top: 0; background: var(--page)",
  mainStyle: "overflow: visible; flex: 1 0 auto",
  pagesStyle: "padding: 0; max-width: none",
  bodyStyle: "flex: 1 0 auto",
  sidebarStyle: "position: sticky; top: 56px; height: calc(100dvh - 56px)",
  panelStyle: "overflow: clip",
  monoStyle: "font-family: var(--family-mono)",
  sheetStyle: "position: fixed",
  // The board's fifth tab is not built.
  tabsStyle: "position: sticky; bottom: 0; grid-template-columns: repeat(4, 1fr)",
  unbuilt: true,
  placeholder: "visibility: hidden",
  // An entry the board draws as a <div> or <a> without an address, given a button's role: Enter and Space press it.
  press: (event: KeyEvent) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      (event.currentTarget as HTMLElement).click();
    }
  },
};
const frameStyle = "overflow: clip; min-height: 100dvh";

// The router's link where the board draws an <a>: it marks the current page with aria-current and loads the page's
// chunk when pointed at.
function RouterLink({ href, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) {
  return <Link to={href} activeOptions={{ exact: href === "/" }} {...props} />;
}

function usePage(): Page | undefined {
  const matchRoute = useMatchRoute();
  return keys.find((key) => {
    const page: { to: string; under?: string[] } = pages[key];
    return [page.to, ...(page.under ?? [])].some((to) => matchRoute({ to, fuzzy: to !== "/" }));
  });
}

const popovers = ["bal", "help", "me", "more"] as const;
type Popover = (typeof popovers)[number];

// One popover is open at a time, on the page it was opened on: following a link closes it. A press outside every
// popover and its button closes it; Escape closes it and hands focus back to its button.
function usePopover() {
  const pathname = useLocation({ select: (location) => location.pathname });
  const [state, setState] = useState<{ name: Popover; at: string } | null>(null);
  const opener = useRef<HTMLElement | null>(null);
  const open = state?.at === pathname ? state.name : null;
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (!(event.target as Element).closest('[data-shell="popover"], [aria-expanded]')) setState(null);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setState(null);
        opener.current?.focus();
      }
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  const toggle = (name: Popover) => (event: SyntheticEvent) => {
    opener.current = event.currentTarget as HTMLElement;
    setState(open === name ? null : { name, at: pathname });
  };
  return {
    ...Object.fromEntries(popovers.map((name) => [`pop_${name}`, open === name ? "open" : ""])),
    balanceOpen: open === "bal",
    helpOpen: open === "help",
    meOpen: open === "me",
    moreOpen: open === "more",
    moreC: open === "more" ? "on" : "",
    oBal: toggle("bal"),
    oHelp: toggle("help"),
    oMe: toggle("me"),
    oMore: toggle("more"),
  };
}

// An amount as the board sets it in the balance popover: the currency, the whole units large, the fraction small.
function parts(money: string) {
  const [, currency = "", whole = money, fraction = ""] = /^(.*?)(\d[\d,]*)(\.\d+.*)?$/.exec(money) ?? [];
  return { balanceCurrency: currency, balanceWhole: whole, balanceFraction: fraction };
}

export function Layout() {
  const { t } = useTranslation();
  const site = useQuery(siteQuery);
  // One sign-out for the menu's button and the notice that says it failed.
  const signOut = useSignOut();
  const notices = (
    <>
      <NewVersionNotice />
      <SignOutError error={signOut.error} />
    </>
  );
  return (
    <Gate stage="member">
      <BoardShell banner={notices} forbiddenHint={t("forbiddenHint")} brandColor={site.data?.brand_color}>
        <ConsoleShell
          name={site.data?.name}
          logoUrl={site.data?.logo_url}
          signOut={() => signOut.isPending || signOut.mutate()}
        >
          <Outlet />
        </ConsoleShell>
      </BoardShell>
    </Gate>
  );
}

// The entries of the bar, sidebar, tabs and menus: where each leads, whether the session may open it, which is current,
// and whether the sidebar shows the account's entries.
function usePageValues() {
  const session = useSession();
  const navigate = useNavigate();
  const page = usePage();
  const level = page && account.includes(page) ? "M" : "P";
  const email = session.user.email;
  return {
    level: `lv${level} pg-${page ?? "none"}`,
    ...Object.fromEntries(
      keys.flatMap((key) => {
        const { to, permission }: { to: string; permission?: string } = pages[key];
        return [
          [`to_${key}`, to],
          [`hide_${key}`, permission !== undefined && !can(session, permission)],
          [`sn_${key}`, key === page ? "on" : ""],
        ];
      }),
    ),
    meOn: level === "M" ? "on" : "",
    meBack: () => navigate({ to: "/" }),
    payOpen: () => navigate({ to: "/balance" }),
    meN: email.split("@")[0],
    meE: email,
    meI: email.slice(0, 1).toUpperCase(),
    meC: 0,
  };
}

// The account menu's theme switch and language entry, showing the page's own.
function usePreferenceValues() {
  const preferences = usePreferences();
  const theme = preferences.themes.indexOf(preferences.theme);
  return {
    locale: preferences.locale,
    dark: preferences.dark ? " thD" : "",
    tsi: theme,
    ...Object.fromEntries(preferences.themes.map((_, i) => [`tsg${i}`, i === theme ? "on" : ""])),
    th0: () => preferences.chooseTheme("system"),
    th1: () => preferences.chooseTheme("light"),
    th2: () => preferences.chooseTheme("dark"),
    themeSystem: theme === 0,
    themeLight: theme === 1,
    themeDark: theme === 2,
    go_prefs: preferences.other.choose,
    languageTag: preferences.other.locale,
    language: preferences.other.name,
  };
}

// The wide sidebar folds below 1024px until the person unfolds it.
function useSidebarValues() {
  const { t } = useTranslation();
  const wide = useMedia(wideWidth);
  const [collapsed, setCollapsed] = useState<boolean>();
  const folded = collapsed ?? !wide;
  return {
    cold: folded ? "cold" : "",
    tgCol: () => setCollapsed(!folded),
    sidebarOpen: !folded,
    colT: folded ? t("shell.expand") : t("shell.collapse"),
  };
}

// The balance as the bar shows it, or what stands in for it while it loads or when it cannot be read.
function useBalanceText() {
  const { t } = useTranslation();
  const session = useSession();
  const format = useFormat();
  const balance = useQuery({ ...balanceQuery, enabled: can(session, "balance.view") });
  const shown = balance.isError
    ? t("shell.balanceUnavailable")
    : balance.data
      ? format.money(balance.data.available)
      : "—";
  return { balance: shown, ...parts(shown) };
}

// ConsoleShell is the AxApp board's shell with the session's data: the bar with the path, the balance, help and the
// account menu, the sidebar on wide windows and the tabs on phones. What fails to load is said in its place; the rest
// of the shell works without it.
function ConsoleShell({
  name,
  logoUrl,
  signOut,
  children,
}: {
  name?: string;
  logoUrl?: string;
  signOut: () => void;
  children: ReactNode;
}) {
  const { t, i18n } = useTranslation();
  const { page, slot } = useKeptPage(children);
  const phone = useMedia(phoneWidth);
  const { level, ...pageValues } = usePageValues();
  const { dark, language, locale, ...preferenceValues } = usePreferenceValues();
  const balance = useBalanceText();
  const values = {
    ...frame,
    ...pageValues,
    ...preferenceValues,
    ...usePopover(),
    ...useSidebarValues(),
    rootC: `${level} ${drawn}${dark}`,
    home: name ?? t("brand", { ns: "common" }),
    path: t("shell.path"),
    navigation: t("shell.navigation", { ns: "common" }),
    fnOut: signOut,
  };
  const english = i18n.language === "en";
  const presetLabels = {
    mf1de1e2621: english ? "Request presets" : "命名预设",
    mb9670c85a4: english ? "Routing presets" : "路由预设",
  };
  const mark = <BoardMark name={name} logoUrl={logoUrl} />;
  // The page comes after the board, so the slot holds the page's element before the page's own effects run.
  return (
    <>
      {phone ? (
        <Phone
          t={(key) => (presetLabels as Record<string, string>)[key] ?? (english ? phoneEn : phoneZh)[key]}
          values={values}
          text={{ language, lang: locale, frameStyle }}
          link={RouterLink}
          mark={mark}
        >
          {slot}
        </Phone>
      ) : (
        <Wide
          t={(key) => (presetLabels as Record<string, string>)[key] ?? (english ? wideEn : wideZh)[key]}
          values={values}
          text={{ language, lang: locale, frameStyle, ...balance }}
          link={RouterLink}
          mark={mark}
        >
          {slot}
        </Wide>
      )}
      {page}
    </>
  );
}
