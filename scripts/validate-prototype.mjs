import { readFileSync } from "node:fs";

const html = readFileSync(new URL("../docs/dayorder-prototype.html", import.meta.url), "utf8");
const script = html.match(/<script>([\s\S]*)<\/script>/)?.[1];
if (!script) throw new Error("prototype script is missing");
new Function(script);

const ids = [...html.matchAll(/id="([^"]+)"/g)].map((match) => match[1]);
const duplicates = ids.filter((id, index) => ids.indexOf(id) !== index);
if (duplicates.length) throw new Error(`duplicate prototype ids: ${[...new Set(duplicates)].join(", ")}`);

console.log("prototype script syntax and IDs valid");
