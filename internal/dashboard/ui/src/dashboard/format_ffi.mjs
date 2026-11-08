export const fixed = (value, places) => value.toFixed(places);
export const timestamp = ms => {
  if (!ms) return "";
  const date = new Date(ms);
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map(v => String(v).padStart(2, "0")).join(":") + "." + String(date.getMilliseconds()).padStart(3, "0");
};
export const parse_time = value => {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : -1;
};
export const input_time = ms => {
  if (!ms) return "";
  const date = new Date(ms);
  if (!Number.isFinite(date.getTime())) return "";
  return new Date(ms - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
export const timezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;
export const parse_zone = (value, utc) => {
  if (!value) return -1;
  const parsed = Date.parse(utc ? value.replace(/Z$/, "") + "Z" : value);
  // Reject nonexistent local times rather than silently shifting across DST.
  if (!Number.isFinite(parsed)) return -1;
  const roundtrip = input_zone(parsed, utc);
  return roundtrip === value.slice(0, 16) ? parsed : -1;
};
export const input_zone = (ms, utc) => {
  if (ms <= 0 || !Number.isFinite(ms)) return "";
  return utc ? new Date(ms).toISOString().slice(0, 16) : input_time(ms);
};
