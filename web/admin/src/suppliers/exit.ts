// chosenExit is the exit a key's form asks for (suppliers.md「出口代理」): the proxy chosen, or null for direct. Before
// the proxies are listed the form offers no choice, and that is not a choice of direct: it is undefined, nothing to send.
export function chosenExit(listed: boolean, value: string | undefined): string | null | undefined {
  if (!listed) return undefined;
  return value || null;
}

// egressRead is the form's answer: the exit to send, or the problem of having none yet. Direct is the value null, so
// only undefined (no choice offered) waits; a ?? there read direct as waiting and kept a key on its proxy.
export function egressRead(
  listed: boolean,
  value: string | undefined,
  waiting: string,
): string | null | { problem: string } {
  const exit = chosenExit(listed, value);
  return exit === undefined ? { problem: waiting } : exit;
}
