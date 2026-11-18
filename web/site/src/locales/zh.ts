import first from "@axiom/boards/Site/regions/FirstScreen.zh";
import headline from "@axiom/boards/Site/regions/Headline.zh";
import wide from "@axiom/boards/Site/regions/Shell.zh";
import narrow from "@axiom/boards/SitePhone/regions/Shell.zh";

// The website's messages in Chinese, the source every other language follows.
export default {
  home: "{{site}} 首页",
  navigation: "主导航",
  models: "模型与价格",
  signIn: "登录",
  // the home page's words, by the keys the board's regions give them (packages/boards/manifest/site.ts)
  board: { ...wide, ...narrow, ...first, ...headline },
  // the language as its own switch names it
  language: "简体中文",
  // the sections of the home page from the top, as its island names them
  sections: {
    surface: "海面",
    connect: "接入",
    models: "模型与线路",
    steps: "三步开始",
    production: "上生产前",
    faq: "常见问题",
    seabed: "海底",
  },
  theme: {
    toDark: "切换到深色",
    toLight: "切换到浅色",
  },
  footer: "© Axiom · 本站中文使用了 MiSans 字体",
};
