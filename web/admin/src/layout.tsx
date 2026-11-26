import "@axiom/boards/AxApp/regions.css";
import { can, Gate, NewVersionNotice, SignOutError, useSession, useSignOut } from "@axiom/app";
import { useWaiting } from "@axiom/app/tanstack";
import {
  AdminShell,
  type AdminShellText,
  BoardShell,
  CommandPalette,
  type ShellEntry,
  type ShellGroup,
  type ShellLink,
  searchKeys,
} from "@axiom/ui";
import { Link, Outlet, useLocation } from "@tanstack/react-router";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAdminBrand } from "./brand";
import { groups, type NavEntry, phoneTabs, settingsEntry, settingsGroups } from "./nav";
import { usePaletteSections } from "./palette";
import { useBell, usePending } from "./pending";

// The router's link where the shell draws one. An address may carry a query (the bell's alert), which the router takes
// apart. The shell marks the current page itself, so the router's own mark is only ever on an exact match.
const RouterLink: ShellLink = ({ href, ...props }) => {
  const url = new URL(href, window.location.origin);
  return (
    <Link
      to={url.pathname}
      search={url.search ? Object.fromEntries(url.searchParams) : undefined}
      activeOptions={{ exact: true }}
      {...props}
    />
  );
};

// A page is under an entry when it is the entry's own route or below it; the dashboard only on its own.
const under = (pathname: string, to: string) =>
  to === "/" ? pathname === "/" : pathname === to || pathname.startsWith(`${to}/`);

const owns = (entry: NavEntry, pathname: string) =>
  under(pathname, entry.to) || Boolean(entry.also?.some((page) => under(pathname, page.to)));

// The navigation the session may use, in the shell's terms: each entry's name, whether it is open, and its count.
function useNavigation() {
  const { t } = useTranslation();
  const session = useSession();
  const pathname = useLocation({ select: (location) => location.pathname });
  const pending = usePending();
  const entry = (item: NavEntry): ShellEntry => ({
    href: item.to,
    label: t(`shell.nav.${item.key}`),
    icon: item.icon,
    current: owns(item, pathname),
    count: item.pending ? pending[item.pending] : undefined,
  });
  const visible = (list: readonly { label: string; entries: readonly NavEntry[] }[]) =>
    list.flatMap((group): ShellGroup[] => {
      const entries = group.entries.filter((item) => can(session, item.permission)).map(entry);
      return entries.length ? [{ label: group.label, entries }] : [];
    });
  const main = visible(groups.map((group) => ({ ...group, label: t(`shell.groups.${group.key}`) })));
  const settings = can(session, settingsEntry.permission)
    ? {
        entry: entry(settingsEntry),
        groups: visible(settingsGroups.map((group) => ({ ...group, label: t(`shell.settingsGroups.${group.key}`) }))),
      }
    : undefined;
  const all = groups.flatMap((group) => group.entries);
  const tabs = phoneTabs
    .flatMap((key) => all.filter((item) => item.key === key && can(session, item.permission)))
    .slice(0, 4)
    .map((item) => ({ ...entry(item), label: t(`shell.tabs.${item.key as (typeof phoneTabs)[number]}`) }));
  return { pathname, main, settings, tabs };
}

// The page the settings' 返回 goes back to: the last one outside the settings, or the first page.
function useBack(pathname: string, inSettings: boolean, home: string) {
  const back = useRef(home);
  useEffect(() => {
    if (!inSettings) back.current = pathname;
  }, [pathname, inSettings]);
  return inSettings ? back.current : pathname;
}

function useShellText(): AdminShellText {
  const { t } = useTranslation();
  return {
    navigation: t("shell.navigation"),
    settingsNavigation: t("shell.settingsNavigation"),
    home: t("shell.home"),
    search: t("shell.search"),
    searchHint: t("shell.searchHint"),
    alerts: t("shell.alerts"),
    alertsWithCount: (count) => t("shell.alertsWithCount", { count }),
    unresolved: t("shell.unresolved"),
    noAlerts: t("shell.noAlerts"),
    allAlerts: t("shell.allAlerts"),
    alertRules: t("shell.alertRules"),
    help: t("shell.help"),
    shortcuts: t("shell.shortcuts"),
    account: t("shell.account"),
    appearance: t("shell.appearance"),
    themes: { system: t("shell.themes.system"), light: t("shell.themes.light"), dark: t("shell.themes.dark") },
    signOut: t("shell.signOut"),
    back: t("shell.back"),
    more: t("shell.more"),
    collapse: t("shell.collapse"),
    expand: t("shell.expand"),
    pending: (count) => t("shell.pending", { count }),
  };
}

export function Layout() {
  const { t } = useTranslation();
  const signOut = useSignOut();
  const waiting = useWaiting();
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
        waiting={waiting}
      >
        <Shell signOut={() => signOut.isPending || signOut.mutate()}>
          <Outlet />
        </Shell>
      </BoardShell>
    </Gate>
  );
}

function Shell({ signOut, children }: { signOut: () => void; children: ReactNode }) {
  const { t } = useTranslation();
  const session = useSession();
  const brand = useAdminBrand();
  const nav = useNavigation();
  const text = useShellText();
  const [searching, setSearching] = useState(false);
  const home = nav.main[0]?.entries[0]?.href ?? "/";
  const inSettings = Boolean(nav.settings?.groups.some((group) => group.entries.some((entry) => entry.current)));
  const back = useBack(nav.pathname, inSettings, home);
  const bell = useBell((id) => `/alert-events?event=${encodeURIComponent(id)}`);
  const sections = usePaletteSections(nav, signOut, searching);
  const email = session.user.email;
  return (
    <AdminShell
      brand={{ name: brand.name, logoUrl: brand.logo_url }}
      homeHref={home}
      groups={nav.main}
      settings={nav.settings && { ...nav.settings, backHref: back }}
      tabs={nav.tabs}
      alerts={
        can(session, "sites.manage")
          ? { ...bell, allHref: "/alert-events", rulesHref: "/alert-configuration" }
          : undefined
      }
      account={{ name: email.split("@")[0] ?? email, email }}
      shortcuts={[
        { keys: searchKeys(), label: t("shell.keys.palette") },
        { keys: "↑ ↓", label: t("shell.keys.move") },
        { keys: "↵", label: t("shell.keys.open") },
        { keys: "esc", label: t("shell.keys.close") },
      ]}
      onSearch={() => setSearching(true)}
      onSignOut={signOut}
      link={RouterLink}
      text={text}
    >
      {children}
      <CommandPalette
        open={searching}
        onOpenChange={setSearching}
        sections={sections}
        text={{
          label: t("shell.palette.label"),
          placeholder: t("shell.palette.placeholder"),
          empty: (query) => t("shell.palette.empty", { query }),
          move: t("shell.palette.move"),
          open: t("shell.palette.open"),
          close: t("shell.palette.close"),
        }}
      />
    </AdminShell>
  );
}
