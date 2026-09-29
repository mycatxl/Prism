import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * Joins class names and resolves Tailwind conflicts.
 *
 * `twMerge` matters because component variants and call-site overrides are both
 * plain class strings: without it, a `p-2` in a default variant silently wins over
 * the `p-6` a caller passed.
 */
export function cn(...values: ClassValue[]): string {
  return twMerge(clsx(values));
}