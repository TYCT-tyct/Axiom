import { type RefCallback, useCallback, useRef, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";

export type ThemeChoice = "system" | "light" | "dark";

// Match Shell's 820px wide/phone switch in layout.tsx so page and frame stay on the same board family.
const wideQuery = "(min-width: 820px)";

export function useWide(): boolean {
  return useSyncExternalStore(
    (notify) => {
      const list = window.matchMedia(wideQuery);
      list.addEventListener("change", notify);
      return () => list.removeEventListener("change", notify);
    },
    () => window.matchMedia(wideQuery).matches,
    () => true,
  );
}

export function useEnglish(): boolean {
  const { i18n } = useTranslation();
  return (i18n.resolvedLanguage ?? i18n.language).startsWith("en");
}

// Replicate .pin.qpages padding because layout.tsx zeros page padding; display:block shows cross-board regions
// (e.g. Security cards rendered inside /settings or /notifications) that CSS otherwise hides by page class.
export function boardPageStyle(wide: boolean, opts?: { first?: boolean; last?: boolean }): string {
  const pt = opts?.first ? (wide ? 30 : 14) : 0;
  const px = wide ? 40 : 16;
  const pb = opts?.last ? (wide ? 96 : 120) : 0;
  return `display: block; width: 100%; max-width: 1180px; margin: 0 auto; padding: ${pt}px ${px}px ${pb}px; overflow-wrap: anywhere;`;
}

// Identify the clicked row inside a generated card regardless of whether preceding rows are tags or buttons.
export function clickedRowIndex(target: EventTarget | null): number {
  if (!(target instanceof Element)) return -1;
  const card = target.closest(".qcard");
  const row = target.closest(".qrow, .qses, .qnt:not(.hd)");
  if (!card || !row) return -1;
  return Array.from(card.querySelectorAll(".qrow, .qses, .qnt:not(.hd)")).indexOf(row);
}

export function makeBoardT<K extends string>(
  english: boolean,
  en: Record<K, string>,
  zh: Record<K, string>,
  overrides?: Partial<Record<K, string>>,
): (key: K) => string {
  return (key) => overrides?.[key] ?? (english ? en[key] : zh[key]);
}

export type InputFieldSpec = {
  type?: string;
  inputMode?: string;
  autoComplete?: string;
  placeholder?: string;
  maxLength?: number;
  onBlur?: (el: HTMLInputElement) => void;
};

// Configure native attributes and blur handlers for generated <Profile> inputs using a stable React 19 ref callback.
export function useBoardInputs(specs: Record<string, InputFieldSpec>) {
  const specsRef = useRef(specs);
  specsRef.current = specs;
  const elementsRef = useRef(new Map<string, HTMLInputElement>());

  const inputRef: RefCallback<Element> = useCallback((node) => {
    if (!(node instanceof HTMLInputElement)) return;
    const key = node.dataset.k ?? "";
    const spec = specsRef.current[key];
    if (spec?.type) node.type = spec.type;
    if (spec?.inputMode) node.inputMode = spec.inputMode;
    if (spec?.autoComplete) node.setAttribute("autocomplete", spec.autoComplete);
    if (spec?.placeholder !== undefined) node.placeholder = spec.placeholder;
    if (spec?.maxLength !== undefined) node.maxLength = spec.maxLength;
    elementsRef.current.set(key, node);
    const handleBlur = () => specsRef.current[key]?.onBlur?.(node);
    node.addEventListener("blur", handleBlur);
    return () => {
      node.removeEventListener("blur", handleBlur);
      if (elementsRef.current.get(key) === node) elementsRef.current.delete(key);
    };
  }, []);

  const setValue = useCallback((key: string, value: string) => {
    const el = elementsRef.current.get(key);
    if (el && el.value !== value) el.value = value;
  }, []);

  const setDisabled = useCallback((key: string, disabled: boolean) => {
    const el = elementsRef.current.get(key);
    if (el) el.disabled = disabled;
  }, []);

  return { inputRef, setValue, setDisabled };
}
