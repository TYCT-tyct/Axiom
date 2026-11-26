import { PageBody, PageHead, type PageTab, PageTabs } from "@axiom/ui";
import { useRouter } from "@tanstack/react-router";
import type { ReactNode } from "react";

// The pages the design gives tabs under their title (adTabDef in admin_core.js), each its own route with the tab in the
// address (`?tab=`); the first tab is the page's own address, so it carries none.
type TabbedRoute = "/models" | "/routing" | "/pricing";

type Tab<K extends string> = { key: K; label: string; count?: number };

// useTabs makes the items of a page's tab row: each leads to the page with that tab, and a plain click is routed in
// the app. A tab leaves behind what belongs to the one before (its filters, its open row).
export function useTabs<K extends string>(route: TabbedRoute, tabs: readonly Tab<K>[], current: K): PageTab[] {
  const router = useRouter();
  const first = tabs[0]?.key;
  return tabs.map((tab) => {
    const search = { tab: tab.key === first ? undefined : tab.key };
    return {
      label: tab.label,
      count: tab.count,
      current: tab.key === current,
      href: router.buildLocation({ to: route, search } as never).href,
      onSelect: () => void router.navigate({ to: route, search } as never),
    };
  });
}

// TabbedPage is a page's head, its row of tabs and the body of the tab that is open.
export function TabbedPage({
  title,
  lede,
  actions,
  label,
  tabs,
  children,
}: {
  title: string;
  lede?: string;
  actions?: ReactNode;
  label: string;
  tabs: readonly PageTab[];
  children: ReactNode;
}) {
  return (
    <>
      <PageHead title={title} lede={lede} actions={actions} />
      <PageBody>
        <PageTabs label={label} items={tabs} />
        {children}
      </PageBody>
    </>
  );
}
