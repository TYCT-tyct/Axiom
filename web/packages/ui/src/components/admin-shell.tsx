import { type ComponentType, type ReactNode, useState, useSyncExternalStore } from "react";
import { board } from "../board-classes";
import { classes } from "../classes";
import type { Theme } from "../preferences";
import s from "./admin-shell.module.css";
import { Avatar } from "./avatar";
import { BoardMark, usePreferences } from "./board-shell";
import { Dialog, SidePanel } from "./dialog";
import { Glyph, type GlyphName } from "./glyph";
import { Icon } from "./icon";
import { NavGroup } from "./nav-group";
import { Popover } from "./popover";
import { Segmented } from "./segmented";
import { type Shortcut, ShortcutList } from "./shortcut-list";

// One page of the navigation: where it is, what it is called, its icon, whether it is the page open now, and how many
// things on it wait for someone (shown only when the count is known and above zero).
export type ShellEntry = { href: string; label: string; icon: GlyphName; current: boolean; count?: number };
export type ShellGroup = { label: string; entries: readonly ShellEntry[] };
// An alert the bell lists; href opens it.
type ShellAlert = { key: string; title: string; detail: string; href: string; severe: boolean };

// The app's router link: the shell never imports a router, it renders whatever link the app gives it.
export type ShellLink = ComponentType<{
  href: string;
  className?: string;
  "aria-current"?: "page";
  "aria-label"?: string;
  onClick?: () => void;
  children: ReactNode;
}>;

export type AdminShellText = {
  navigation: string;
  settingsNavigation: string;
  home: string;
  search: string;
  searchHint: string;
  alerts: string;
  alertsWithCount: (count: number) => string;
  unresolved: string;
  noAlerts: string;
  allAlerts: string;
  alertRules: string;
  help: string;
  shortcuts: string;
  account: string;
  appearance: string;
  themes: Record<Theme, string>;
  signOut: string;
  back: string;
  more: string;
  collapse: string;
  expand: string;
  pending: (count: number) => string;
};

export type AdminShellProps = {
  brand: { name: string; logoUrl?: string };
  homeHref: string;
  groups: readonly ShellGroup[];
  // The system settings: the entry at the sidebar's foot, and the navigation that replaces the sidebar's while one of
  // its pages is open, with the way back to the page the person came from.
  settings?: { entry: ShellEntry; groups: readonly ShellGroup[]; backHref: string };
  // The phone's bottom entries before 更多.
  tabs: readonly ShellEntry[];
  // The bell; absent when the session may not see alerts.
  alerts?: { items: readonly ShellAlert[]; count: number; allHref: string; rulesHref?: string };
  account: { name: string; email: string };
  shortcuts: readonly Shortcut[];
  onSearch: () => void;
  onSignOut: () => void;
  link: ShellLink;
  text: AdminShellText;
  children: ReactNode;
};

const subscribeMedia = (query: string) => (changed: () => void) => {
  const list = matchMedia(query);
  list.addEventListener("change", changed);
  return () => list.removeEventListener("change", changed);
};

function useMedia(query: string) {
  return useSyncExternalStore(subscribeMedia(query), () => matchMedia(query).matches);
}

// The board's three widths (adMode in admin_core.js): a phone under 600px, a tablet under 1100px whose sidebar folds to
// an icon rail, and a desktop. The rail stays as the person last set it, at either width.
function useLayout() {
  const phone = useMedia("(max-width: 599px)");
  const wide = useMedia("(min-width: 1100px)");
  const [folded, setFolded] = useState<boolean>();
  const rail = folded ?? !wide;
  return { phone, rail, toggle: () => setFolded(!rail) };
}

// The palette's keys as this computer names them, for the search button and the keyboard help.
export const searchKeys = () => (/Mac|iPhone|iPad/.test(navigator.platform) ? "⌘K" : "Ctrl K");

// AdminShell is the admin board's frame (adShell in admin_core.js): the top bar with search, the bell, help and the
// account; the sidebar in four named groups, which folds to an icon rail and gives way to the settings' own navigation;
// and on a phone the bottom entries with 更多. The document scrolls (docs/frontend.md「外壳与滚动」); the sidebar is
// sticky under the bar and scrolls itself. The page sits inside the board's root (.board.ax.ov) for the board-styled
// parts it is built from.
export function AdminShell(props: AdminShellProps) {
  const { phone, rail, toggle } = useLayout();
  const settingsOpen = Boolean(props.settings?.groups.some((group) => group.entries.some((entry) => entry.current)));
  return (
    <div className={classes(board.board, board.ax, board.ov)}>
      <div className={classes(board.fr, s.root)} data-shell="app" data-rail={!phone && rail ? "" : undefined}>
        <TopBar {...props} phone={phone} />
        <div className={s.body} data-shell="body">
          {phone ? null : <Sidebar {...props} rail={rail} settingsOpen={settingsOpen} onToggle={toggle} />}
          <main id="main" tabIndex={-1} className={s.main}>
            {props.children}
          </main>
        </div>
        {phone ? <PhoneTabs {...props} /> : null}
      </div>
    </div>
  );
}

function TopBar(props: AdminShellProps & { phone: boolean }) {
  const { link: Link, text } = props;
  const keys = searchKeys();
  return (
    <header className={s.bar}>
      <Link href={props.homeHref} className={s.logo} aria-label={text.home}>
        <span className={s.mark}>
          <BoardMark name={props.brand.name} logoUrl={props.brand.logoUrl} />
        </span>
      </Link>
      {props.phone ? (
        <span className={s.spacer} />
      ) : (
        <button type="button" className={s.search} onClick={props.onSearch} aria-keyshortcuts="Meta+K Control+K">
          <Glyph name="search" />
          <span className={s.hint}>{text.searchHint}</span>
          <span className={s.short}>{text.search}</span>
          <kbd className={s.kbd}>{keys}</kbd>
        </button>
      )}
      <div className={s.tools}>
        {props.phone ? (
          <button type="button" className={s.iconButton} aria-label={text.search} onClick={props.onSearch}>
            <Glyph name="search" />
          </button>
        ) : null}
        {props.alerts ? <Bell {...props} alerts={props.alerts} /> : null}
        <Help text={text} shortcuts={props.shortcuts} />
        <Account {...props} />
      </div>
    </header>
  );
}

function Bell({ alerts, link: Link, text }: AdminShellProps & { alerts: NonNullable<AdminShellProps["alerts"]> }) {
  const label = alerts.count ? text.alertsWithCount(alerts.count) : text.alerts;
  return (
    <Popover
      label={text.unresolved}
      align="end"
      trigger={(trigger) => (
        <button type="button" {...trigger} className={s.iconButton} aria-label={label}>
          <Glyph name="bell" />
          <i className={s.ping} data-on={alerts.count ? "" : undefined} aria-hidden="true" />
        </button>
      )}
    >
      {(close) => (
        <div className={s.menu} data-wide="">
          <span className={s.menuHead}>{text.unresolved}</span>
          {alerts.items.length ? (
            alerts.items.map((alert) => (
              <Link key={alert.key} href={alert.href} className={s.alert} onClick={close}>
                <span className={s.tone} data-severe={alert.severe ? "" : undefined} aria-hidden="true" />
                <span className={s.alertText}>
                  <b>{alert.title}</b>
                  <span>{alert.detail}</span>
                </span>
              </Link>
            ))
          ) : (
            <p className={s.none}>{text.noAlerts}</p>
          )}
          <i className={s.rule} />
          <Link href={alerts.allHref} className={s.item} onClick={close}>
            <Glyph name="bell" />
            {text.allAlerts}
          </Link>
          {alerts.rulesHref ? (
            <Link href={alerts.rulesHref} className={s.item} onClick={close}>
              <Glyph name="settings" />
              {text.alertRules}
            </Link>
          ) : null}
        </div>
      )}
    </Popover>
  );
}

// Help holds the keyboard shortcuts, in a dialog of their own (adA_keys).
function Help({ text, shortcuts }: { text: AdminShellText; shortcuts: readonly Shortcut[] }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Popover
        label={text.help}
        align="end"
        trigger={(trigger) => (
          <button type="button" {...trigger} className={s.iconButton} aria-label={text.help}>
            <Glyph name="help" />
          </button>
        )}
      >
        {(close) => (
          <div className={s.menu}>
            <button
              type="button"
              className={s.item}
              onClick={() => {
                close();
                setOpen(true);
              }}
            >
              <Glyph name="search" />
              {text.shortcuts}
            </button>
          </div>
        )}
      </Popover>
      {open ? (
        <Dialog title={text.shortcuts} onClose={() => setOpen(false)}>
          <ShortcutList items={shortcuts} />
        </Dialog>
      ) : null}
    </>
  );
}

function Account({ account, text, onSignOut }: AdminShellProps) {
  const preferences = usePreferences();
  return (
    <Popover
      label={text.account}
      align="end"
      trigger={(trigger) => (
        <button type="button" {...trigger} className={s.avatar} aria-label={text.account}>
          <Avatar name={account.name} />
        </button>
      )}
    >
      {(close) => (
        <div className={s.menu} data-wide="">
          <div className={s.who}>
            <Avatar name={account.name} size="lg" />
            <span className={s.alertText}>
              <b>{account.name}</b>
              <span>{account.email}</span>
            </span>
          </div>
          <i className={s.rule} />
          <button type="button" className={s.item} lang={preferences.other.locale} onClick={preferences.other.choose}>
            <Glyph name="link" />
            {preferences.other.name}
          </button>
          <i className={s.rule} />
          <span className={s.menuHead}>{text.appearance}</span>
          <div className={s.themes}>
            <Segmented
              label={text.appearance}
              value={preferences.theme}
              options={preferences.themes.map((theme) => ({ value: theme, label: text.themes[theme] }))}
              onChange={preferences.chooseTheme}
            />
          </div>
          <i className={s.rule} />
          <button
            type="button"
            className={classes(s.item, s.quiet)}
            onClick={() => {
              close();
              onSignOut();
            }}
          >
            {text.signOut}
          </button>
        </div>
      )}
    </Popover>
  );
}

function Entry({
  entry,
  link: Link,
  rail,
  text,
  tile = false,
  onClick,
}: {
  entry: ShellEntry;
  link: ShellLink;
  rail: boolean;
  text: AdminShellText;
  tile?: boolean;
  onClick?: () => void;
}) {
  const count = entry.count ? text.pending(entry.count) : undefined;
  return (
    <Link
      href={entry.href}
      className={classes(s.entry, tile && s.tile)}
      aria-current={entry.current ? "page" : undefined}
      onClick={onClick}
    >
      <Glyph name={entry.icon} size={20} selected={entry.current} />
      <span className={s.label}>
        {entry.label}
        {rail && count ? <span className={s.tipCount}>{count}</span> : null}
      </span>
      {count ? (
        <span className={s.count} role="img" aria-label={count}>
          {entry.count}
        </span>
      ) : null}
    </Link>
  );
}

function Sidebar(props: AdminShellProps & { rail: boolean; settingsOpen: boolean; onToggle: () => void }) {
  const { settings, text, rail } = props;
  const main = !(props.settingsOpen && settings);
  return (
    <aside className={s.sidebar}>
      <nav
        key={main ? "main" : "settings"}
        className={s.nav}
        aria-label={main ? text.navigation : text.settingsNavigation}
      >
        {main ? <MainNav {...props} /> : <SettingsNav {...props} settings={settings} />}
      </nav>
      <button
        type="button"
        className={classes(s.entry, s.fold)}
        aria-expanded={!rail}
        aria-label={rail ? text.expand : text.collapse}
        onClick={props.onToggle}
      >
        <Glyph name="collapse" size={20} />
        <span className={s.label}>{rail ? text.expand : text.collapse}</span>
      </button>
    </aside>
  );
}

function Groups({
  groups,
  ...rest
}: {
  groups: readonly ShellGroup[];
  link: ShellLink;
  rail: boolean;
  text: AdminShellText;
}) {
  return groups.map((group) => (
    <NavGroup key={group.label} label={group.label} showLabel={!rest.rail}>
      {group.entries.map((entry) => (
        <Entry key={entry.href} entry={entry} {...rest} />
      ))}
    </NavGroup>
  ));
}

function MainNav({ groups, settings, link: Link, rail, text }: AdminShellProps & { rail: boolean }) {
  return (
    <>
      <Groups groups={groups} link={Link} rail={rail} text={text} />
      {settings ? (
        <div className={s.foot}>
          <Link href={settings.entry.href} className={s.entry}>
            <Glyph name={settings.entry.icon} size={20} />
            <span className={s.label}>{settings.entry.label}</span>
            <span className={s.chevron}>
              <Icon name="right" />
            </span>
          </Link>
        </div>
      ) : null}
    </>
  );
}

function SettingsNav({
  settings,
  link: Link,
  rail,
  text,
}: AdminShellProps & { rail: boolean; settings: NonNullable<AdminShellProps["settings"]> }) {
  return (
    <>
      <Link href={settings.backHref} className={classes(s.entry, s.back)}>
        <span className={s.backArrow}>
          <Icon name="left" />
        </span>
        <span className={s.label}>{text.back}</span>
      </Link>
      <Groups groups={settings.groups} link={Link} rail={rail} text={text} />
    </>
  );
}

// The phone's bottom entries, then 更多, which lists every page in the same groups and the settings (adA_more).
function PhoneTabs(props: AdminShellProps) {
  const { tabs, link: Link, text } = props;
  const [more, setMore] = useState(false);
  const elsewhere = !tabs.some((tab) => tab.current);
  return (
    <nav className={s.tabs} data-shell="tabs" aria-label={text.navigation}>
      {tabs.map((tab) => (
        <Link key={tab.href} href={tab.href} className={s.tab} aria-current={tab.current ? "page" : undefined}>
          <Glyph name={tab.icon} size={20} selected={tab.current} />
          <span>{tab.label}</span>
          {tab.count ? (
            <em className={s.tabCount} role="img" aria-label={text.pending(tab.count)}>
              {tab.count}
            </em>
          ) : null}
        </Link>
      ))}
      <button
        type="button"
        className={s.tab}
        aria-expanded={more}
        data-on={elsewhere ? "" : undefined}
        onClick={() => setMore(true)}
      >
        <Glyph name="more" size={20} selected={elsewhere} />
        <span>{text.more}</span>
      </button>
      {more ? <MoreSheet {...props} onClose={() => setMore(false)} /> : null}
    </nav>
  );
}

function MoreSheet({ groups, settings, link, text, onClose }: AdminShellProps & { onClose: () => void }) {
  const all = settings
    ? [...groups, { label: settings.entry.label, entries: settings.groups.flatMap((group) => group.entries) }]
    : groups;
  return (
    <SidePanel drawer title={text.more} onClose={onClose}>
      {all.map((group) => (
        <NavGroup key={group.label} label={group.label} tiles>
          {group.entries.map((entry) => (
            <Entry key={entry.href} entry={entry} link={link} rail={false} text={text} tile onClick={onClose} />
          ))}
        </NavGroup>
      ))}
    </SidePanel>
  );
}
