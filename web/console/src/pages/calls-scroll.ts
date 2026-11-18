import type { CallFilters } from "../queries";

// Preserve scroll position and search filters across /calls -> /calls/$callId -> /calls navigation.
const memoryStore = new Map<string, number>();
const storagePrefix = "axiom:calls-scroll:";
const lastSearchKey = "axiom:calls-last-search";
let lastSearchMemory: (CallFilters & { group_by?: "day" | "model" | "key"; tab?: "sessions"; call?: string }) | null =
  null;

export function saveCallsScroll(searchKey: string, y: number) {
  memoryStore.set(searchKey, y);
  try {
    globalThis.sessionStorage?.setItem(`${storagePrefix}${searchKey}`, String(Math.round(y)));
  } catch {
    // Ignore storage quota or private-mode restrictions; in-memory entry still works for SPA back/forward.
  }
}

export function hasSavedCallsScroll(): boolean {
  if (memoryStore.size > 0) return true;
  try {
    const s = globalThis.sessionStorage;
    if (!s) return false;
    for (let i = 0; i < s.length; i++) {
      if (s.key(i)?.startsWith(storagePrefix)) return true;
    }
  } catch {
    // Ignore storage restrictions.
  }
  return false;
}

export function takeCallsScroll(searchKey: string): number | null {
  const mem = memoryStore.get(searchKey);
  memoryStore.delete(searchKey);
  let stored: number | null = null;
  try {
    const raw = globalThis.sessionStorage?.getItem(`${storagePrefix}${searchKey}`);
    if (raw !== null && raw !== undefined) {
      globalThis.sessionStorage?.removeItem(`${storagePrefix}${searchKey}`);
      const parsed = Number(raw);
      if (Number.isFinite(parsed) && parsed >= 0) stored = parsed;
    }
  } catch {
    // Ignore storage access restrictions.
  }
  if (typeof mem === "number" && Number.isFinite(mem) && mem >= 0) return mem;
  return stored;
}

export function saveLastCallsSearch(
  search: CallFilters & { group_by?: "day" | "model" | "key"; tab?: "sessions"; call?: string },
) {
  lastSearchMemory = search;
  try {
    globalThis.sessionStorage?.setItem(lastSearchKey, JSON.stringify(search));
  } catch {
    // Ignore storage restrictions.
  }
}

export function getLastCallsSearch():
  | (CallFilters & { group_by?: "day" | "model" | "key"; tab?: "sessions"; call?: string })
  | undefined {
  if (lastSearchMemory) return lastSearchMemory;
  try {
    const raw = globalThis.sessionStorage?.getItem(lastSearchKey);
    if (!raw) return undefined;
    return JSON.parse(raw) as CallFilters & { group_by?: "day" | "model" | "key"; tab?: "sessions"; call?: string };
  } catch {
    return undefined;
  }
}
