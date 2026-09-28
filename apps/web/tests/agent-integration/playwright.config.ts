import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.DAYORDER_AGENT_HARNESS_URL?.trim();
if (!baseURL) throw new Error("DAYORDER_AGENT_HARNESS_URL is required; refusing to use a production default");

const realProvider = process.env.DAYORDER_AGENT_PROVIDER_MODE === "real";

export default defineConfig({
  testDir: ".",
  testMatch: realProvider ? "real-provider.spec.ts" : ["readonly.spec.ts", "failures.spec.ts"],
  testIgnore: realProvider ? [] : ["real-provider.spec.ts"],
  fullyParallel: false,
  workers: 1,
  timeout: 75_000,
  expect: { timeout: 10_000 },
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  reporter: "line",
});
