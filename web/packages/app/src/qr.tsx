import { QrMatrix } from "@axiom/ui";
import { useEffect, useMemo, useState } from "react";

const quietZone = 4;

type Encoder = typeof import("qrcode-generator");

// The encoder comes the first time a code is drawn, not with the first screen: RecoveryCodesHost, which every page
// mounts, lives beside the dialogs that draw codes. Until it comes the code's square stands empty, at its size.
let encoder: Promise<Encoder> | undefined;
const loadEncoder = () => (encoder ??= import("qrcode-generator").then((module) => module.default));

// QrCode draws a text as a QR code in SVG, dark on light whatever the theme, because a phone's camera reads it that way.
// Each dark module is a square of one path, so there is no markup built from strings.
export function QrCode({ value, label }: { value: string; label: string }) {
  const [qrcode, setQrcode] = useState<Encoder>();
  useEffect(() => {
    let live = true;
    void loadEncoder().then((loaded) => live && setQrcode(() => loaded));
    return () => {
      live = false;
    };
  }, []);
  const { size, path } = useMemo(() => {
    if (!qrcode) return { size: 1, path: "" };
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
  }, [qrcode, value]);
  return <QrMatrix size={size} path={path} label={label} />;
}
