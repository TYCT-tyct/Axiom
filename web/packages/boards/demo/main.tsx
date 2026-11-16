import { createRoot } from "react-dom/client";
import "../src/demo-fonts.css";

const theme = new URLSearchParams(location.search).get("theme") === "dark" ? "深色" : "浅色";
const fonts = ["400 16px 'Axiom Sans'", "400 16px 'Axiom CJK'", "400 16px 'Axiom Mono'", "600 40px 'AX Serif'"];
await Promise.all(fonts.map((font) => document.fonts.load(font)));
const root = createRoot(document.getElementById("root") as HTMLElement);
const path = location.pathname;
// Load only this route before rendering: Suspense's reveal timer cannot run under the artboard's paused clock.
if (path === "/Login") {
  const { default: Login } = await import("../src/Login/Board.tsx");
  root.render(<Login mode={theme} />);
} else if (path === "/ClawdEgg") {
  const { default: ClawdEgg } = await import("../src/ClawdEgg/Board.tsx");
  root.render(<ClawdEgg mode={theme} />);
} else {
  const { default: AxApp } = await import("../src/AxApp/Board.tsx");
  root.render(
    <AxApp
      theme={theme}
      ref={(board) => {
        if (board) board.go("ov");
      }}
    />,
  );
}
