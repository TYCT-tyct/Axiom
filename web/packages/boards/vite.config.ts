import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { contentSecurityPolicy } from "../../vite.shared.ts";
import { sourceFont, sources } from "./fonts.ts";

export default defineConfig({
  // Alpha colors must retain authored precision: .5 and #80 differ at the text's translucent backdrop edges.
  build: { cssMinify: false },
  plugins: [
    react(),
    {
      name: "board-demo-fonts",
      async buildStart() {
        for (const name of Object.keys(sources))
          this.emitFile({ type: "asset", fileName: `fonts/${name}`, source: await sourceFont(name) });
      },
      configureServer(server) {
        server.middlewares.use(async (req, res, next) => {
          const name = req.url?.match(/^\/fonts\/([\w-]+)$/)?.[1];
          if (!name || !sources[name]) return next();
          res.setHeader("Content-Type", "font/ttf");
          res.end(await sourceFont(name));
        });
      },
    },
  ],
  preview: { headers: { "Content-Security-Policy": contentSecurityPolicy } },
});
