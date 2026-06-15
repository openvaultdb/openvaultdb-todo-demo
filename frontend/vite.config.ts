import { defineConfig } from "vite";

// The dev server runs on the fixed port the integration contract pins (5173).
export default defineConfig({
  server: {
    port: 5173,
    strictPort: true,
  },
});
