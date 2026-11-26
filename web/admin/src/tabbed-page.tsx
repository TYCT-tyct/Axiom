import { PageBody, PageHead, type PageTab, PageTabs } from "@axiom/ui";
import { useRouter } from "@tanstack/react-router";
import type { ReactNode } from "react";

// The pages the design gives tabs under their title (adTabDef in admin_core.js), each its own route with the tab in the
// address (`?tab=`); the first tab is the page's own address, so it carries none.
type TabbedRoute = "/models" | "/routing" | "/pricing" | "/suppliers" | "/suppliers/$supplierId";

type Tab<K extends string> = { key: K; label: string; count?: number };

// useTabs makes the items of a page's tab row: each leads to the page with that tab, and a plain click is routed in
// the app. A tab leaves behind what belongs to the one before (its filters, its open row). A page of one thing (a
// channel) gives the params that name it.
export function useTabs<K extends string>(
  route: TabbedRoute,
  tabs: readonly Tab<K>[],
  current: K,
  params?: Record<string, string>,
): PageTab[] {
  const router = useRouter();
  const first = tabs[0]?.key;
  return tabs.map((tab) => {
    const search = { tab: tab.key === first ? undefined : tab.key };
    return {
      label: tab.label,
      count: tab.count,
      current: tab.key === current,
      href: router.buildLocation({ to: route, params, search } as never).href,
      onSelect: () => void router.navigate({ to: route, params, search } as never),
    };
  });
}

// TabbedPage is a page's head, its row of tabs and the body of the tab that is open.
export function TabbedPage({
  title,
  lede,
  actions,
  tag,
  label,
  tabs,
  children,
}: {
  title: string;
  lede?: string;
  actions?: ReactNode;
  tag?: ReactNode;
  label: string;
  tabs: readonly PageTab[];
  children: ReactNode;
}) {
  return (
    <PageBody>
      <PageHead title={title} lede={lede} actions={actions} tag={tag} />
      <PageTabs label={label} items={tabs} />
      {children}
    </PageBody>
  );
}
