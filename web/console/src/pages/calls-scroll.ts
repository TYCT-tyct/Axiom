import type { CallFilters } from "../queries";
import type { PresetChoice } from "./calls-data";

export type SavedCallsSearch = CallFilters & {
  preset?: PresetChoice;
  group_by?: "day" | "model" | "key";
  tab?: "sessions";
  call?: string;
};

type SavedCallsNavigation = {
  identity: string;
  search: SavedCallsSearch;
  searchStr: string;
  scrollY: number;
  armed: boolean;
};

const navStore = new Map<string, SavedCallsNavigation>();
const storagePrefix = "axiom:calls-nav:";

function storageKey(identity: string, navId: string) {
  return `${storagePrefix}${encodeURIComponent(identity)}:${navId}`;
}

export function newCallsNavId() {
  if (typeof globalThis.crypto?.randomUUID === "function") return globalThis.crypto.randomUUID();
  return `nav-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

function readEntry(navId: string, identity: string): SavedCallsNavigation | null {
  if (!navId || !identity) return null;
  const mem = navStore.get(navId);
  if (mem && mem.identity === identity) return mem;
  try {
    const raw = globalThis.sessionStorage?.getItem(storageKey(identity, navId));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as SavedCallsNavigation;
    if (parsed.identity !== identity) return null;
    navStore.set(navId, parsed);
    return parsed;
  } catch {
    return null;
  }
}

function writeEntry(navId: string, entry: SavedCallsNavigation) {
  navStore.set(navId, entry);
  try {
    const s = globalThis.sessionStorage;
    if (!s) return;
    // Drop entries from other accounts in the same browser tab.
    for (let i = s.length - 1; i >= 0; i--) {
      const key = s.key(i);
      if (key?.startsWith(storagePrefix) && !key.startsWith(`${storagePrefix}${encodeURIComponent(entry.identity)}:`)) {
        s.removeItem(key);
      }
    }
    s.setItem(storageKey(entry.identity, navId), JSON.stringify(entry));
  } catch {
    // Ignore quota or storage restrictions; in-memory entry still serves SPA navigation.
  }
}

export function saveCallsNavigation(
  navId: string,
  identity: string,
  search: SavedCallsSearch,
  searchStr: string,
  scrollY: number,
) {
  if (!navId || !identity) return;
  writeEntry(navId, {
    identity,
    search,
    searchStr,
    scrollY: Math.max(0, Math.round(scrollY)),
    armed: false,
  });
}

export function getCallsReturnState(navId: string | undefined, identity: string) {
  if (!navId || !identity) return { search: undefined, hasScroll: false };
  const entry = readEntry(navId, identity);
  if (!entry) return { search: undefined, hasScroll: false };
  return { search: entry.search, hasScroll: entry.scrollY > 0 };
}

export function armCallsReturnScroll(navId: string | undefined, identity: string) {
  if (!navId || !identity) return;
  const entry = readEntry(navId, identity);
  if (!entry) return;
  writeEntry(navId, { ...entry, armed: true });
}

export function takeCallsScroll(navId: string | undefined, identity: string, searchStr: string): number | null {
  if (!identity) return null;
  if (navId) {
    const entry = readEntry(navId, identity);
    if (!entry || entry.searchStr !== searchStr || !entry.armed) return null;
    writeEntry(navId, { ...entry, armed: false });
    return entry.scrollY;
  }
  return null;
}
