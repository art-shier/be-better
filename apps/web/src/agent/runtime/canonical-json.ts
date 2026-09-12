function encodeCanonicalJSON(value: unknown): string {
  if (value === null || typeof value === "boolean" || typeof value === "number" || typeof value === "string") {
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map(encodeCanonicalJSON).join(",")}]`;
  }

  const object = value as Record<string, unknown>;
  const fields = Object.keys(object)
    .sort()
    .map((key) => `${JSON.stringify(key)}:${encodeCanonicalJSON(object[key])}`);
  return `{${fields.join(",")}}`;
}

// canonicalJSONString first snapshots JavaScript values through the host JSON
// serializer, then emits object keys in ECMAScript UTF-16 code-unit order.
// Arrays retain their order and primitive spellings follow JSON.stringify.
export function canonicalJSONString(value: unknown): string | undefined {
  const snapshot = JSON.stringify(value);
  if (snapshot === undefined) return undefined;
  return encodeCanonicalJSON(JSON.parse(snapshot));
}
