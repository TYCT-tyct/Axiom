import { type PageTab, PageTabs, SidePanel } from "@axiom/ui";
import { useNavigate, useRouter, useSearch } from "@tanstack/react-router";
import { type ReactNode, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// What a list page with a drawer keeps in its address: the row whose drawer is open (selected), so a reload, Back and a
// pasted link keep it, and the search the list is narrowed by.
type Opened = { selected?: string; q?: string };

// useSelected is the open row and the way to open or close one. Opening adds a history entry, so Back closes the
// drawer; stepping to the next row rewrites the current one, so holding J does not fill the history.
export function useSelected() {
  const navigate = useNavigate();
  const { selected } = useSearch({ strict: false }) as Opened;
  const select = (id: string | undefined, how: "open" | "step" = "open") =>
    navigate({
      to: ".",
      search: ((search: Record<string, unknown>) => ({ ...search, selected: id })) as never,
      replace: how === "step",
    });
  return [selected, select] as const;
}

type Select = ReturnType<typeof useSelected>[1];

// What a drawer of a row read by its ID is given: the ID, the rows around it, and the way to open another.
export type IdDrawer = { id: string; keys: readonly string[]; select: Select };

// RowDrawer is a list row's detail over the list (the admin's adPanel): the previous and next rows of the list as it
// stands, "2 / 6", J and K, and Esc to close. keys are the rows' keys in the order they are listed; a row outside them
// (a link to one the list has not loaded) opens without them. A drawer whose row is still being read is this same drawer with
// a placeholder title, so stepping to a row not read yet keeps the one drawer open rather than closing and opening it.
export function RowDrawer({
  title,
  subtitle,
  keys,
  selected,
  select,
  footer,
  wide,
  children,
}: {
  title: string;
  subtitle?: string;
  keys: readonly string[];
  selected: string;
  select: Select;
  footer?: ReactNode;
  wide?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const index = keys.indexOf(selected);
  const next = (by: -1 | 1) => {
    const key = keys[index + by];
    if (key !== undefined) void select(key, "step");
  };
  return (
    <SidePanel
      drawer
      wide={wide}
      title={title}
      subtitle={subtitle}
      onClose={() => void select(undefined)}
      steps={
        index < 0 || keys.length < 2
          ? undefined
          : {
              index,
              count: keys.length,
              onStep: next,
              previousLabel: t("drawerSteps.previous"),
              nextLabel: t("drawerSteps.next"),
            }
      }
      footer={footer}
    >
      {children}
    </SidePanel>
  );
}

// useAddressSearch is a search box whose text goes to the address (commit) a moment after the typing stops, and which
// follows the address when it changes from outside. The server is asked once per pause, not once per key.
function useAddressSearch(address: string | undefined, commit: (text: string | undefined) => void) {
  const [text, setText] = useState(address ?? "");
  const [seen, setSeen] = useState(address);
  if (address !== seen) {
    setSeen(address);
    setText(address ?? "");
  }
  useEffect(() => {
    const next = text.trim() || undefined;
    if (next === address) return;
    const timer = setTimeout(() => commit(next), 300);
    return () => clearTimeout(timer);
  }, [text, address, commit]);
  return [text, setText] as const;
}

// useAddressParam is a list's search box tied to one parameter of the address (the email's start in q, a note's in note): the
// text typed, and the address's value it leads to. Searching closes the open row, as the list under it changes.
export function useAddressParam(name: string) {
  const navigate = useNavigate();
  const address = (useSearch({ strict: false }) as Record<string, string | undefined>)[name];
  const commit = useCallback(
    (text: string | undefined) =>
      void navigate({
        to: ".",
        search: ((search: Record<string, unknown>) => ({ ...search, [name]: text, selected: undefined })) as never,
        replace: true,
      }),
    [navigate, name],
  );
  const [text, setText] = useAddressSearch(address, commit);
  return { address, text, setText };
}

// RouteTabs is a page's tabs, each a route: a link to its address, and a plain press routes in the app.
export function RouteTabs<K extends string>({
  label,
  tabs,
  current,
}: {
  label: string;
  tabs: readonly { key: K; to: string; label: string; count?: number }[];
  current: K;
}) {
  const router = useRouter();
  const items: PageTab[] = tabs.map((tab) => ({
    label: tab.label,
    count: tab.count,
    current: tab.key === current,
    href: router.buildLocation({ to: tab.to }).href,
    onSelect: () => void router.navigate({ to: tab.to }),
  }));
  return <PageTabs label={label} items={items} />;
}
