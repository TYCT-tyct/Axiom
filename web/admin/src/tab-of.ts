// tabOf reads the tab of an address: one of the page's other tabs, or none, which is the first. Whatever else an address
// names reads as none, so a hand-edited link cannot put the page on a tab it has not got. It sits apart from the tab row
// (tabbed-page.tsx) because the router reads it before any page is loaded.
export function tabOf<K extends string>(value: unknown, others: readonly K[]): K | undefined {
  return others.find((key) => key === value);
}
