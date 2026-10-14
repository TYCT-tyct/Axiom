import "./main.css";
import { startApp } from "@axiom/ui";
import { Home } from "./home";
import en from "./locales/en";
import zh from "./locales/zh";

startApp("site", { zh, en }, <Home />);
