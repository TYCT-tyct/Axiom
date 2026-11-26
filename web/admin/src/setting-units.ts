// A number of a settings row is edited in the unit its row names (天, 分钟, 百分比) while the API keeps seconds, basis
// points or billionths. The text shows the stored value in that unit, with the decimals it needs and no more, and what
// is typed is read back to the stored whole number; a value that is not left alone is never rounded on the way.

const unitSeconds = { second: 1, minute: 60, hour: 3_600, day: 86_400 } as const;
export type TimeUnit = keyof typeof unitSeconds;

const places = 6;

// inUnit writes a stored count of seconds in a unit: 172800 in days is "2", 1800 in days "0.020833".
export function inUnit(total: number, unit: TimeUnit): string {
  return String(Number((total / unitSeconds[unit]).toFixed(places)));
}

// secondsOf reads typed text in a unit back to whole seconds; undefined when it is not a plain decimal number.
export function secondsOf(text: string, unit: TimeUnit): number | undefined {
  const typed = text.trim();
  return new RegExp(`^\\d+(\\.\\d{1,${places}})?$`).test(typed)
    ? Math.round(Number(typed) * unitSeconds[unit])
    : undefined;
}

// countOf reads typed digits as a whole number.
export function countOf(text: string): number | undefined {
  const typed = text.trim();
  return /^\d+$/.test(typed) && Number.isSafeInteger(Number(typed)) ? Number(typed) : undefined;
}

// scaledOf reads typed text such as "12.5" as a whole number of 1/scale units: 12.5 percent is 1250 basis points at a
// scale of 100, which is also the most decimals it may have.
export function scaledOf(text: string, scale: number): number | undefined {
  const digits = String(scale).length - 1;
  const typed = text.trim();
  return new RegExp(`^\\d+(\\.\\d{1,${digits}})?$`).test(typed) ? Math.round(Number(typed) * scale) : undefined;
}

// scaledText writes a whole number of 1/scale units as the text scaledOf reads: 1250 at a scale of 100 is "12.5".
export function scaledText(value: number, scale: number): string {
  return String(value / scale);
}

export const within = (value: number | undefined, min: number, max: number): value is number =>
  value !== undefined && value >= min && value <= max;
