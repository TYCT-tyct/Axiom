import type { KeyboardEvent } from "react";
import { pressBoardButton } from "../board-interaction";
import { classes } from "../classes";
import s from "./page-tabs.module.css";

// A tab leads to an address (href, with onSelect to route it in the app) or only does something (onSelect).
export type PageTab = { label: string; count?: number; current: boolean } & (
  | { href: string; onSelect?: () => void }
  | { href?: never; onSelect: () => void }
);

const moves: Partial<Record<string, (at: number, last: number) => number>> = {
  ArrowRight: (at, last) => (at === last ? 0 : at + 1),
  ArrowLeft: (at, last) => (at === 0 ? last : at - 1),
  Home: () => 0,
  End: (_, last) => last,
};

// PageTabs is the row of in-page text tabs under a page's title (adTabs in admin_core.js), each with an optional count
// badge. It is one tab stop (role=tablist, roving tabindex on the current tab); the left and right keys, Home and End
// move between the tabs, and Enter or Space opens one, so moving never navigates by itself. It knows no router: a tab
// with an href is a link, and onSelect, when given, takes a plain click instead of the browser.
export function PageTabs({ label, items }: { label: string; items: readonly PageTab[] }) {
  const move = (event: KeyboardEvent<HTMLElement>) => {
    const tabs = [...event.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]')];
    const at = tabs.indexOf(event.target as HTMLElement);
    const to = moves[event.key]?.(at, tabs.length - 1);
    if (at < 0 || to === undefined) return;
    event.preventDefault();
    tabs[to]?.focus();
  };
  return (
    <div role="tablist" aria-label={label} className={s.root} onKeyDown={move}>
      {items.map((item) => (
        <Tab key={item.label} item={item} />
      ))}
    </div>
  );
}

function Tab({ item }: { item: PageTab }) {
  const shared = {
    role: "tab",
    "aria-selected": item.current,
    tabIndex: item.current ? 0 : -1,
    className: classes(s.tab, item.current && s.current),
  } as const;
  const words = (
    <>
      {item.label}
      {item.count ? <em className={s.count}>{item.count}</em> : null}
    </>
  );
  if (item.href === undefined) {
    return (
      <button type="button" {...shared} onClick={item.onSelect}>
        {words}
      </button>
    );
  }
  const { onSelect } = item;
  return (
    <a
      {...shared}
      href={item.href}
      onKeyDown={(event) => event.key === " " && pressBoardButton(event)}
      onClick={(event) => {
        if (!onSelect || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        onSelect();
      }}
    >
      {words}
    </a>
  );
}
