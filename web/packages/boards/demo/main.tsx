import { lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import "../src/demo-fonts.css";

const Login = lazy(() => import("../src/Login/Board.tsx"));
const ClawdEgg = lazy(() => import("../src/ClawdEgg/Board.tsx"));
const AxApp = lazy(() => import("../src/AxApp/Board.tsx"));
const theme = new URLSearchParams(location.search).get("theme") === "dark" ? "深色" : "浅色";
const fonts = ["400 16px 'Axiom Sans'", "400 16px 'Axiom CJK'", "400 16px 'Axiom Mono'", "600 40px 'AX Serif'"];
await Promise.all(fonts.map((font) => document.fonts.load(font)));
const root = createRoot(document.getElementById("root") as HTMLElement);
const path = location.pathname;
root.render(
  <Suspense>
    {path === "/Login" ? (
      <Login mode={theme} />
    ) : path === "/ClawdEgg" ? (
      <ClawdEgg mode={theme} />
    ) : (
      <AxApp
        theme={theme}
        ref={(board) => {
          if (board) board.go("ov");
        }}
      />
    )}
  </Suspense>,
);
