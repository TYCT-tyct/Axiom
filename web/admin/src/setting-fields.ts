import { countOf, inUnit, secondsOf, type TimeUnit, within } from "./setting-units";
import type { SettingValue } from "./settings-drafts";

// A number of the API that a settings row edits: the field it is in, the bounds the contract states in the API's own
// units, and the unit the row is written in (seconds are edited as days, minutes or hours; without a unit it is a
// count). The text of a row is the stored number in that unit.
export type NumberSpec = { name: string; min: number; max: number; unit?: TimeUnit };

export function numberText(spec: NumberSpec, stored: number | undefined): string {
  if (stored === undefined) return "";
  return spec.unit ? inUnit(stored, spec.unit) : String(stored);
}

// numberOf is what a row's text stands for in the API's units: undefined when empty (the value is left to its default)
// or when it is not a number within bounds, which numberValid tells apart.
export function numberOf(spec: NumberSpec, text: string): number | undefined {
  const typed = text.trim();
  const value = spec.unit ? secondsOf(typed, spec.unit) : countOf(typed);
  return within(value, spec.min, spec.max) ? value : undefined;
}

// numberValid is whether a row's text can be sent: a number within bounds, or empty where the default in code may stand
// in for it.
export const numberValid = (spec: NumberSpec, text: SettingValue, optional: boolean) =>
  typeof text === "string" && ((optional && text.trim() === "") || numberOf(spec, text) !== undefined);

// numberPatch is the API's numbers a page's rows changed, by field name: the rows whose text differs from what was saved,
// each read back to the API's unit, an emptied one as undefined (the default). The rows left alone are not in it, so what
// the server holds for them, set or not, is neither rounded nor turned into a value nobody chose.
export function numberPatch(
  specs: Record<string, NumberSpec>,
  values: Record<string, SettingValue>,
  saved: Record<string, SettingValue>,
): Record<string, number | undefined> {
  const patch: Record<string, number | undefined> = {};
  for (const [key, spec] of Object.entries(specs)) {
    if (values[key] === saved[key]) continue;
    patch[spec.name] = numberOf(spec, String(values[key]));
  }
  return patch;
}
