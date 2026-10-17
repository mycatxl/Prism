/**
 * Generates the client-side idempotency key a purchase carries.
 *
 * `crypto.randomUUID` only exists in a secure context, and this console is
 * routinely opened over plain HTTP on a LAN address - the same gap that made
 * `navigator.clipboard` throw in `lib/clipboard.ts`. There it was a copy that
 * silently did nothing; here it would be a purchase that never submits.
 *
 * `crypto.getRandomValues` *is* available outside a secure context, so the
 * fallback builds a version-4 UUID from it. Uniqueness is all this needs to be:
 * the server treats the value as an opaque key and never parses it.
 */
export function newRequestID(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }

  const bytes = new Uint8Array(16);
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i += 1) bytes[i] = Math.floor(Math.random() * 256);
  }
  // Version 4 and the RFC 4122 variant, which is what a UUID reader expects.
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;

  const hex = [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
