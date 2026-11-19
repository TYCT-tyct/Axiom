import s from "./qr-matrix.module.css";

// QrMatrix draws a QR code that is already worked out: its side in modules, quiet zone included, and one path of
// squares for the dark ones. Working it out needs a QR encoder, which the app that scans a secret brings.
export function QrMatrix({ size, path, label }: { size: number; path: string; label: string }) {
  return (
    <svg viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label} className={s.root}>
      <path d={path} className={s.modules} shapeRendering="crispEdges" />
    </svg>
  );
}
