import { defineConfig } from "vite";

// Deliberately unsupported: assumes Bun's automatic dotenv loading supplies
// process.env.PORT to an external `vite` script. On Bun 1.4.2 it does not.
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.PORT ?? 5173),
    strictPort: true,
  },
});
