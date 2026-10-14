// Browser storage throws in some private modes and when site data is blocked; a preference that cannot be read or kept
// falls back to its default instead of breaking the page.
export function readStored(storage: () => Storage, key: string): string | null {
  try {
    return storage().getItem(key);
  } catch {
    return null;
  }
}

export function writeStored(storage: () => Storage, key: string, value: string) {
  try {
    storage().setItem(key, value);
  } catch {
    // the preference lasts until the page closes
  }
}
