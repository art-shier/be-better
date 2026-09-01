import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import http from "node:http";
import path from "node:path";
import test from "node:test";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

const execFileAsync = promisify(execFile);
const scriptsDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.dirname(scriptsDirectory);

function writeJSON(response, status, body, headers = {}) {
  response.writeHead(status, { "Content-Type": "application/json", ...headers });
  response.end(JSON.stringify(body));
}

test("load smoke registers an API-supported device platform", async () => {
  let registeredPlatform = "";
  const server = http.createServer(async (request, response) => {
    const body = await new Promise((resolve) => {
      let value = "";
      request.setEncoding("utf8");
      request.on("data", (chunk) => { value += chunk; });
      request.on("end", () => resolve(value ? JSON.parse(value) : {}));
    });

    if (request.method === "POST" && request.url === "/api/v1/auth/login") {
      writeJSON(response, 200, {}, { "Set-Cookie": "dayorder_session=test; Path=/; HttpOnly" });
      return;
    }
    if (request.method === "PUT" && request.url.startsWith("/api/v1/users/me/devices/")) {
      registeredPlatform = body.platform;
      const supported = new Set(["web", "windows", "macos", "linux", "ios", "android"]);
      if (!supported.has(registeredPlatform)) {
        writeJSON(response, 422, { error: { code: "VALIDATION_FAILED" } });
        return;
      }
      writeJSON(response, 201, { device: { id: request.url.split("/").at(-1), ...body } });
      return;
    }
    if (request.method === "POST" && request.url === "/api/v1/tasks") {
      writeJSON(response, 201, { id: "task-1", version: 1 });
      return;
    }
    if (request.method === "GET" && request.url === "/api/v1/tasks/task-1") {
      writeJSON(response, 200, { id: "task-1", version: 1 });
      return;
    }
    if (request.method === "PATCH" && request.url === "/api/v1/tasks/task-1") {
      writeJSON(response, 200, { id: "task-1", version: 2 });
      return;
    }
    if (request.method === "DELETE" && request.url === "/api/v1/tasks/task-1") {
      response.writeHead(204);
      response.end();
      return;
    }
    writeJSON(response, 404, { error: { code: "NOT_FOUND" } });
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const address = server.address();
    await execFileAsync(process.execPath, [
      path.join(scriptsDirectory, "load-smoke.js"),
      "--base-url", `http://127.0.0.1:${address.port}`,
      "--email", "load-smoke@example.invalid",
      "--password", "test-password",
      "--cycles", "1",
      "--concurrency", "1",
      "--p95-ms", "10000",
    ], { cwd: repositoryRoot });
  } finally {
    await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }

  assert.ok(["web", "windows", "macos", "linux", "ios", "android"].includes(registeredPlatform));
});
