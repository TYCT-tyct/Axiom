import "./main.css";
import { crossDocuments, prepareArrival } from "@axiom/fx";
import { startApp } from "@axiom/ui";
import { Home } from "./home";
import en from "./locales/en";
import zh from "./locales/zh";

// The website has two pages; a plain path switch keeps the console's router out of its bundle, and loading /models on
// demand keeps the AxApp shared stylesheet out of the public home page.
crossDocuments();
prepareArrival();
const models = window.location.pathname.replace(/\/+$/, "").endsWith("/models");
if (models) {
  void import("./models").then(({ Models }) => startApp("site", { zh, en }, <Models />));
} else {
  startApp("site", { zh, en }, <Home />);
}
