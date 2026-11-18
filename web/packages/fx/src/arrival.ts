// The one-shot marker of an arrival through the website's tiles departure (port design §9): the website sets it as it
// leaves for the console's sign-in pages, and the first of those pages to open takes it, to gather the tiles and
// decode its title once. It holds only the time it was set, so a marker left by a departure that never arrived (the
// browser stopped, another page opened) has gone stale before a later visit could take it.
const key = "axiom:arrival";
const fresh = 10_000;

export function markArrival() {
  try {
    sessionStorage.setItem(key, String(Date.now()));
  } catch {
    // storage refused (private mode, quota): the sign-in page opens as a direct visit would
  }
}

export function takeArrival(): boolean {
  try {
    const at = Number(sessionStorage.getItem(key));
    sessionStorage.removeItem(key);
    return at > 0 && Date.now() - at < fresh;
  } catch {
    return false;
  }
}
