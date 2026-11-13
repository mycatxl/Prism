import { useId } from "react";
import { cn } from "../lib/cn";

/** A solid optical silhouette at favicon size; glass facets reveal themselves as it grows. */
export function PrismMark({
  size = 32,
  variant = "brand",
  className,
  title,
}: {
  size?: number;
  variant?: "brand" | "onDark";
  className?: string;
  title?: string;
}) {
  const id = useId().replace(/:/g, "");
  const paint = (name: string) => `url(#${id}-${name})`;

  return (
    <svg
      viewBox="0 0 128 128"
      width={size}
      height={size}
      className={cn("prism-mark", className)}
      data-variant={variant}
      role={title ? "img" : undefined}
      aria-hidden={title ? undefined : true}
      focusable="false"
      fill="none"
    >
      {title ? <title>{title}</title> : null}
      <defs>
        <linearGradient id={`${id}-body`} x1="23" y1="35" x2="108" y2="105" gradientUnits="userSpaceOnUse">
          <stop stopColor="#0d9488" />
          <stop offset=".44" stopColor="#22d3ee" />
          <stop offset="1" stopColor="#8b5cf6" />
        </linearGradient>
        <linearGradient id={`${id}-face`} x1="42" y1="28" x2="68" y2="108" gradientUnits="userSpaceOnUse">
          <stop stopColor="#ecfeff" stopOpacity=".92" />
          <stop offset=".48" stopColor="#5eead4" stopOpacity=".24" />
          <stop offset="1" stopColor="#0f766e" stopOpacity=".62" />
        </linearGradient>
        <linearGradient id={`${id}-edge`} x1="62" y1="20" x2="106" y2="95" gradientUnits="userSpaceOnUse">
          <stop stopColor="#cffafe" stopOpacity=".8" />
          <stop offset=".42" stopColor="#38bdf8" stopOpacity=".12" />
          <stop offset="1" stopColor="#6d28d9" stopOpacity=".78" />
        </linearGradient>
        <linearGradient id={`${id}-spectrum`} x1="39" y1="61" x2="114" y2="88" gradientUnits="userSpaceOnUse">
          <stop stopColor="#f0fdfa" stopOpacity=".95" />
          <stop offset=".38" stopColor="#a5f3fc" stopOpacity=".85" />
          <stop offset=".66" stopColor="#38bdf8" stopOpacity=".6" />
          <stop offset="1" stopColor="#c4b5fd" stopOpacity=".9" />
        </linearGradient>
        <radialGradient id={`${id}-light`} cx="0" cy="0" r="1" gradientTransform="translate(58 62) rotate(31) scale(46 34)" gradientUnits="userSpaceOnUse">
          <stop stopColor="#ecfeff" stopOpacity=".75" />
          <stop offset="1" stopColor="#ecfeff" stopOpacity="0" />
        </radialGradient>
        <linearGradient id={`${id}-rim`} x1="40" y1="19" x2="92" y2="111" gradientUnits="userSpaceOnUse">
          <stop stopColor="#fff" stopOpacity=".95" />
          <stop offset=".5" stopColor="#cffafe" stopOpacity=".35" />
          <stop offset="1" stopColor="#ddd6fe" stopOpacity=".7" />
        </linearGradient>
      </defs>
      <path d="M50 21 68 12Q72 10 75 16L117 85Q120 90 115 94L95 110Q93 112 88 111L14 102Q8 101 11 95L46 26Q48 22 50 21Z" fill={paint("body")} />
      <path d="M50 22 92 110 14 101Q10 100 12 95L47 26Q48 23 50 22Z" fill={paint("face")} />
      <path d="m51 22 17-9q4-2 7 3l42 69q3 5-2 9l-22 17Z" fill={paint("edge")} />
      <path d="M50 22 92 110 14 101Z" fill={paint("light")} />
      <path d="m34 58 40 9 37 12 6 10-40-14-47-10Z" fill={paint("spectrum")} />
      <path d="m30 65 47 10 35 23-8 6-30-25Z" fill="#c4b5fd" fillOpacity=".32" />
      <path d="m14 101 61-26 17 35Z" fill="#99f6e4" fillOpacity=".18" />
      <path d="m50 22 42 88m-42-88-38 75q-2 4 3 4l73 10q5 1 7-1l20-16" stroke={paint("rim")} strokeWidth="1.8" strokeLinejoin="round" />
      <path d="m50 22 19-9q3-1 5 3l42 70" stroke="#e0f2fe" strokeOpacity=".65" strokeWidth="1.5" strokeLinecap="round" />
      <path d="m33 61 42 10 40 18" stroke="#ecfeff" strokeOpacity=".8" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  );
}
