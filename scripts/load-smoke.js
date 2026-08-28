import { performance } from "node:perf_hooks";

function option(name, fallback) {
  const index = process.argv.indexOf(`--${name}`);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

const baseUrl = String(option("base-url", process.env.DAYORDER_BASE_URL ?? "")).replace(/\/$/, "");
const email = option("email", process.env.DAYORDER_LOAD_EMAIL);
const password = option("password", process.env.DAYORDER_LOAD_PASSWORD);
const cycles = Number(option("cycles", process.env.DAYORDER_LOAD_CYCLES ?? 20));
const concurrency = Number(option("concurrency", process.env.DAYORDER_LOAD_CONCURRENCY ?? 5));
const p95LimitMs = Number(option("p95-ms", process.env.DAYORDER_LOAD_P95_MS ?? 1000));

if (!baseUrl || !email || !password) {
  throw new Error("Usage: node scripts/load-smoke.js --base-url URL --email EMAIL --password PASSWORD [--cycles 20] [--concurrency 5] [--p95-ms 1000]");
}
if (!Number.isInteger(cycles) || cycles < 1 || !Number.isInteger(concurrency) || concurrency < 1 || p95LimitMs <= 0) {
  throw new Error("cycles and concurrency must be positive integers; p95-ms must be positive");
}

let cookie = "";
const durations = [];
const errors = [];

async function request(path, { expected = [200], ...init } = {}) {
  const started = performance.now();
  let response;
  try {
    response = await fetch(`${baseUrl}${path}`, {
      ...init,
      headers: {
        Accept: "application/json",
        ...(init.body ? { "Content-Type": "application/json" } : {}),
        ...(cookie ? { Cookie: cookie } : {}),
        ...init.headers,
      },
    });
  } catch (error) {
    errors.push(`${init.method ?? "GET"} ${path}: ${error instanceof Error ? error.message : String(error)}`);
    throw error;
  } finally {
    durations.push(performance.now() - started);
  }
  if (!expected.includes(response.status)) {
    const body = await response.text();
    const message = `${init.method ?? "GET"} ${path}: HTTP ${response.status} ${body.slice(0, 200)}`;
    errors.push(message);
    throw new Error(message);
  }
  return response;
}

const login = await request("/api/v1/auth/login", {
  method: "POST",
  expected: [200],
  body: JSON.stringify({ email, password }),
});
cookie = (login.headers.get("set-cookie") ?? "").split(";")[0];
if (!cookie.startsWith("dayorder_session=")) throw new Error("login did not return the session cookie");

const deviceId = crypto.randomUUID();
await request(`/api/v1/users/me/devices/${deviceId}`, {
  method: "PUT",
  expected: [201],
  body: JSON.stringify({ deviceName: "Load smoke", platform: "node" }),
});

let nextCycle = 0;
async function worker() {
  while (true) {
    const cycle = nextCycle++;
    if (cycle >= cycles) return;
    const mutationHeaders = { "X-Device-ID": deviceId, "Idempotency-Key": crypto.randomUUID() };
    const createdResponse = await request("/api/v1/tasks", {
      method: "POST",
      expected: [201],
      headers: mutationHeaders,
      body: JSON.stringify({
        title: `Load smoke ${cycle}`,
        status: "todo",
        priority: "normal",
        estimateMinutes: 15,
      }),
    });
    const created = await createdResponse.json();
    await request(`/api/v1/tasks/${created.id}`);
    const updatedResponse = await request(`/api/v1/tasks/${created.id}`, {
      method: "PATCH",
      expected: [200],
      headers: {
        "Content-Type": "application/merge-patch+json",
        "If-Match": `\"${created.version}\"`,
        "X-Device-ID": deviceId,
        "Idempotency-Key": crypto.randomUUID(),
      },
      body: JSON.stringify({ status: "done" }),
    });
    const updated = await updatedResponse.json();
    await request(`/api/v1/tasks/${created.id}`, {
      method: "DELETE",
      expected: [204],
      headers: {
        "If-Match": `\"${updated.version}\"`,
        "X-Device-ID": deviceId,
        "Idempotency-Key": crypto.randomUUID(),
      },
    });
  }
}

await Promise.all(Array.from({ length: Math.min(concurrency, cycles) }, () => worker()));

const sorted = [...durations].sort((left, right) => left - right);
const percentileIndex = Math.max(0, Math.ceil(sorted.length * 0.95) - 1);
const p95 = sorted[percentileIndex];
const errorRate = errors.length / Math.max(1, durations.length);
console.log(JSON.stringify({ cycles, concurrency, requests: durations.length, errors: errors.length, errorRate, p95Ms: Math.round(p95 * 10) / 10 }));
if (errors.length > 0 || errorRate > 0.01) throw new Error(`load smoke error rate ${(errorRate * 100).toFixed(2)}% exceeded 1%`);
if (p95 > p95LimitMs) throw new Error(`load smoke p95 ${p95.toFixed(1)}ms exceeded ${p95LimitMs}ms`);
