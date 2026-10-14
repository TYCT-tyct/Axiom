import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The design's text sizes (styles.css) are sizes, not colours: without this, text-ui and text-t1 would count as two
// colours and the later one would drop the other.
const twMerge = extendTailwindMerge({ extend: { theme: { text: ["ui", "label"] } } });

// cn joins class names, the later of two conflicting Tailwind utilities winning, so a caller's className overrides a
// component's default.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
