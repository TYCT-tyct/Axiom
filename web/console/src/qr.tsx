import qrcode from "qrcode-generator";
import { useMemo } from "react";

const quietZone = 4;

// QrCode draws a text as a QR code in SVG, dark on light whatever the theme, because a phone's camera reads it that way.
// Each dark module is a square of one path, so there is no markup built from strings.
export function QrCode({ value, label }: { value: string; label: string }) {
  const { size, path } = useMemo(() => {
    const code = qrcode(0, "M");
    code.addData(value);
    code.make();
    const count = code.getModuleCount();
    let squares = "";
    for (let row = 0; row < count; row++) {
      for (let column = 0; column < count; column++) {
        if (code.isDark(row, column)) {
          squares += `M${column + quietZone} ${row + quietZone}h1v1h-1z`;
        }
      }
    }
    return { size: count + 2 * quietZone, path: squares };
  }, [value]);
  return (
    <svg
      viewBox={`0 0 ${size} ${size}`}
      role="img"
      aria-label={label}
      className="size-48 shrink-0 rounded-btn bg-paper"
    >
      <path d={path} className="fill-ink" shapeRendering="crispEdges" />
    </svg>
  );
}
