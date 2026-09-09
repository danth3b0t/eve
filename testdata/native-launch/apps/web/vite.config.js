import { defineConfig, loadEnv } from "vite";

// The baseline application explicitly consumes PORT via Vite's native loader.
// This config is committed before any manual workspace preparation.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  return {
    server: {
      host: "127.0.0.1",
      port: Number(process.env.PORT ?? env.PORT ?? 5173),
      strictPort: true,
    },
  };
});
