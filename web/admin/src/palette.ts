import { can, useSession } from "@axiom/app";
import type { PaletteItem, PaletteSection, ShellEntry, ShellGroup } from "@axiom/ui";
import { usePreferences } from "@axiom/ui";
import { useNavigate } from "@tanstack/react-router";
import i18next from "i18next";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { groups, settingsGroups, settingsRoutes } from "./nav";

type Navigation = { main: ShellGroup[]; settings?: { entry: ShellEntry; groups: ShellGroup[] } };

// The pages the last few navigations left, newest first, the open one aside (adGo's S.recent).
function useRecent(current: string | undefined) {
  const [recent, setRecent] = useState<string[]>([]);
  const [last, setLast] = useState(current);
  if (current !== last) {
    setLast(current);
    if (last) setRecent((was) => [last, ...was.filter((href) => href !== last && href !== current)].slice(0, 4));
  }
  return recent.filter((href) => href !== current).slice(0, 3);
}

// The routes the design folds into a page's tabs, found by the page's name and the tab's (adPalAll's 「页面，分页」).
function useTabPages(entries: ShellEntry[]) {
  const { t } = useTranslation();
  const session = useSession();
  return [...groups, ...settingsGroups].flatMap((group) =>
    group.entries.flatMap((item) => {
      const parent = entries.find((entry) => entry.href === item.to);
      if (!parent || !can(session, item.permission)) return [];
      return (item.also ?? []).flatMap((page) =>
        page.label
          ? [
              {
                ...parent,
                href: page.to,
                label: t("shell.palette.tab", { page: parent.label, tab: t(`shell.also.${page.label}`) }),
              },
            ]
          : [],
      );
    }),
  );
}

// The common actions are the shell's own until the pages add theirs: the appearance, the language, signing out.
function useActions(signOut: () => void): PaletteItem[] {
  const { t } = useTranslation();
  const preferences = usePreferences();
  const meta = t("shell.palette.action");
  return [
    ...preferences.themes
      .filter((theme) => theme !== preferences.theme)
      .map((theme) => ({
        key: `theme:${theme}`,
        label: t("shell.palette.themeTo", { theme: t(`shell.themes.${theme}`) }),
        meta,
        icon: "appearance" as const,
        onSelect: () => preferences.chooseTheme(theme),
      })),
    {
      key: "language",
      label: preferences.other.name,
      meta,
      icon: "link",
      keywords: t("shell.palette.language"),
      onSelect: preferences.other.choose,
    },
    { key: "sign-out", label: t("shell.signOut"), meta, icon: "security", onSelect: signOut },
  ];
}

type SettingSearch = typeof import("./settings-registry").settingSearchItems;

// The registry of settings and their words are loaded when the palette is first opened, not with the first screen: the
// admin's first screen is held to a budget, and nobody searches before opening the palette.
function useSettingSearch(open: boolean): SettingSearch | undefined {
  const [search, setSearch] = useState<SettingSearch>();
  useEffect(() => {
    if (!open || search) return;
    void Promise.all([import("./settings-registry"), import("./locales/settings")]).then(([registry, words]) => {
      for (const language of ["zh", "en"] as const) {
        i18next.addResourceBundle(language, "admin", words.default[language], true);
      }
      setSearch(() => registry.settingSearchItems);
    });
  }, [open, search]);
  return search;
}

// Every registered setting by its name, to its page with the row named so that the page flashes it (adPalAll).
function useSettingItems(open: boolean): PaletteItem[] {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSettingSearch(open);
  return (search?.(t) ?? []).map((setting) => {
    const page = settingsRoutes[setting.page];
    return {
      key: `setting:${setting.key}`,
      label: setting.label,
      meta: t(`shell.nav.${page.nav}`),
      icon: "settings",
      keywords: setting.detail,
      onSelect: () => void navigate({ to: page.to, search: setting.search }),
    };
  });
}

// The palette's sources (adPalItems): with no query, the recent pages, the actions, then every page; a query also
// finds the settings and the tabs' routes.
export function usePaletteSections(nav: Navigation, signOut: () => void, open: boolean): PaletteSection[] {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const item =
    (meta: string) =>
    (entry: ShellEntry): PaletteItem => ({
      key: entry.href,
      label: entry.label,
      meta,
      icon: entry.icon,
      onSelect: () => void navigate({ to: entry.href }),
    });
  const pages = nav.main.flatMap((group) => group.entries);
  const settings = nav.settings?.groups.flatMap((group) => group.entries) ?? [];
  const all = [...pages, ...settings];
  const recent = useRecent(all.find((entry) => entry.current)?.href);
  const tabPages = useTabPages(all);
  const actions = useActions(signOut);
  const settingRows = useSettingItems(open);
  const page = item(t("shell.palette.page"));
  return [
    {
      label: t("shell.palette.recent"),
      idle: true,
      items: recent.flatMap((href) => all.filter((entry) => entry.href === href)).map(page),
    },
    { label: t("shell.palette.actions"), idle: true, items: actions },
    { label: t("shell.palette.pages"), idle: true, items: pages.map(page) },
    { label: t("shell.palette.setting"), items: [...settings.map(item(t("shell.palette.setting"))), ...settingRows] },
    { label: t("shell.palette.pages"), items: tabPages.map(page) },
  ];
}
