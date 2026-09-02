import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const load = (path) => readFile(resolve(root, path), "utf8");
const requireMatch = (text, pattern, message) => {
  if (!pattern.test(text)) throw new Error(message);
};
const rejectMatch = (text, pattern, message) => {
  if (pattern.test(text)) throw new Error(message);
};

const [dockerfile, compose, caddy, postgres, hba, environment, roles, backup] = await Promise.all([
  load("Dockerfile"),
  load("deploy/compose.yaml"),
  load("deploy/Caddyfile"),
  load("deploy/postgres/postgresql.conf"),
  load("deploy/postgres/pg_hba.conf"),
  load("deploy/env.production.example"),
  load("deploy/scripts/bootstrap-roles.sql"),
  load("deploy/pgbackrest/pgbackrest.conf.example"),
]);

for (const target of ["api", "worker", "migrate", "web", "postgres"]) {
  requireMatch(dockerfile, new RegExp(`AS ${target}\\b`, "i"), `Dockerfile is missing ${target} target`);
}
for (const image of ["NODE_IMAGE", "GO_IMAGE", "RUNTIME_IMAGE", "CADDY_IMAGE", "POSTGRES_IMAGE"]) {
  requireMatch(dockerfile, new RegExp(`ARG ${image}=.+@sha256:[a-f0-9]{64}`, "i"), `${image} must be pinned by digest`);
}
requireMatch(compose, /prom\/prometheus:[^\s]+@sha256:[a-f0-9]{64}/i, "Prometheus must be pinned by digest");
requireMatch(compose, /prom\/node-exporter:[^\s]+@sha256:[a-f0-9]{64}/i, "Node Exporter must be pinned by digest");
requireMatch(dockerfile, /^USER\s+[^0]/m, "runtime images must declare a non-root user");
requireMatch(
  dockerfile,
  /setcap\s+-r\s+\/usr\/bin\/caddy/,
  "the unprivileged Caddy image must remove its inherited bind-service capability",
);
requireMatch(compose, /internal:\s*true/, "the data network must be internal");
requireMatch(compose, /read_only:\s*true/g, "runtime containers must use a read-only root filesystem");
requireMatch(compose, /cap_drop:\s*\n\s*-\s*ALL/g, "runtime containers must drop Linux capabilities");
rejectMatch(compose, /postgres:[\s\S]{0,900}?ports:/, "PostgreSQL must not publish a host port");
requireMatch(
  compose,
  /pg_isready\s+-h\s+127\.0\.0\.1\s+-p\s+5432\s+-U\s+\$\$\{POSTGRES_USER\}\s+-d\s+\$\$\{POSTGRES_DB\}/,
  "PostgreSQL health check must wait for the final TCP listener",
);
rejectMatch(compose, /(?:password|secret|token):[ \t]*(?!\$|\/run\/secrets|\{)[^\s#]+/i, "Compose must not contain inline secret values");
requireMatch(caddy, /Strict-Transport-Security/i, "Caddy must emit HSTS");
requireMatch(caddy, /reverse_proxy\s+api:8080/, "Caddy must proxy API traffic");
requireMatch(postgres, /password_encryption\s*=\s*'scram-sha-256'/, "PostgreSQL must use SCRAM");
requireMatch(postgres, /archive_timeout\s*=\s*'5min'/, "WAL archive timeout must satisfy the five-minute RPO");
requireMatch(hba, /scram-sha-256/, "pg_hba must require SCRAM for host connections");
for (const role of ["dayorder_migrator", "dayorder_api", "dayorder_worker", "dayorder_backup", "dayorder_monitor"]) {
  requireMatch(roles, new RegExp(`CREATE ROLE ${role}\\b`, "i"), `role bootstrap is missing ${role}`);
}
requireMatch(roles, /REVOKE CONNECT, TEMPORARY ON DATABASE[^;]+FROM PUBLIC/i, "database defaults must revoke public connect and temporary privileges");
requireMatch(roles, /GRANT pg_monitor TO dayorder_monitor/i, "monitor role must inherit pg_monitor");
requireMatch(backup, /repo1-cipher-type=aes-256-cbc/, "pgBackRest repository must be encrypted");
requireMatch(backup, /compress-type=gz/, "pgBackRest compression must be supported by the Alpine package");
for (const variable of [
  "POSTGRES_PASSWORD_FILE",
  "DATABASE_URL_FILE",
  "WORKER_DATABASE_URL_FILE",
  "MIGRATION_DATABASE_URL_FILE",
  "DAYORDER_AUTH_HMAC_KEY_FILE",
]) {
  requireMatch(environment, new RegExp(`^${variable}=`, "m"), `production environment example is missing ${variable}`);
}
rejectMatch(compose, /DAYORDER_AGENT_/, "disabled Agent configuration must not be present in Compose");
rejectMatch(environment, /^DAYORDER_AGENT_/m, "disabled Agent configuration must not be present in the production environment example");
rejectMatch(environment, /(development-only|change-me|replace-with)/i, "production environment example contains an unsafe placeholder secret");

console.log("Production deployment configuration passes static security checks.");
