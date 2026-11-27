// chosenExit is the exit a key's form asks for (suppliers.md「出口代理」): the proxy chosen, or null for direct. Before
// the proxies are listed the form offers no choice, and that is not a choice of direct: it is undefined, nothing to send.
export function chosenExit(listed: boolean, value: string | undefined): string | null | undefined {
  if (!listed) return undefined;
  return value || null;
}
