import "./main.css";
import { startApp } from "@axiom/ui";
import { Home } from "./home";
import en from "./locales/en";
import zh from "./locales/zh";
import { Models } from "./models";

// The website has two pages; a plain path switch keeps the console's router out of its bundle. The Go server answers
// any unknown path with this document (internal/platform/webui), and /models is the one page besides the home page.
const models = window.location.pathname.replace(/\/+$/, "").endsWith("/models");
startApp("site", { zh, en }, models ? <Models /> : <Home />);
