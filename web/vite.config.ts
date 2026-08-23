import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// base: "./" keeps every asset reference relative, so the built site works
// identically served from Go's embedded httpui adapter, from a plain
// `file://` open (Snapshot export), or from any subpath a Bridge WebView
// might load it from.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
