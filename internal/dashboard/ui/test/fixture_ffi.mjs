import { readFileSync } from "node:fs";
export function snapshot() {
  return readFileSync(new URL("../../../../test/fixtures/snapshot.json", import.meta.url), "utf8");
}
export function spans() {
  return readFileSync(new URL("../../../../test/fixtures/spans.json", import.meta.url), "utf8");
}
