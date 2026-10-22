import { defineConfig } from "@playwright/test";

// E2E against a running stack (docker compose up). BASE_URL defaults to the web container port.
export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  workers: 1,
  use: { baseURL: process.env.BASE_URL ?? "http://localhost:3000", viewport: { width: 1440, height: 900 } },
  reporter: [["list"]],
});
