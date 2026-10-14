// Browser storage throws in some private modes and when site data is blocked, even on reading localStorage itself; a
// preference that cannot be read or kept falls back to its default instead of breaking the page.
export function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // the preference lasts until the page closes
  }
}
