import { cn } from "../lib/cn";

/**
 * The Prism mark: a prism standing on the brand teal, one beam entering the left
 * face and three beams leaving the right. The name is the picture, so the mark
 * has to survive at 16px in a favicon and at 40px on the sign-in card: three
 * shapes, no gradients, no detail thinner than 6/128 units.
 *
 * Two tones, because the mark has two grounds. `brand` is the tile — teal plate,
 * white prism — for light surfaces and the favicon. `onDark` drops the plate and
 * draws the primary strokes in white for the rail, where a teal plate on teal ink
 * would disappear; the outer rays keep a tint so the split still reads.
 */
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
  const onDark = variant === "onDark";
  return (
    <svg
      viewBox="0 0 128 128"
      width={size}
      height={size}
      className={cn("prism-mark", className)}
      role={title ? "img" : undefined}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      {title ? <title>{title}</title> : null}
      {onDark ? null : <rect width="128" height="128" rx="30" fill="#087f78" />}
      <path
        d="M60 22 94 96H26Z"
        fill="none"
        stroke="#ffffff"
        strokeWidth={onDark ? 9 : 8}
        strokeLinejoin="round"
        strokeLinecap="round"
      />
      <path d="M3 60h37" stroke="#ffffff" strokeWidth={6} strokeLinecap="round" />
      <g fill="none" strokeWidth={6} strokeLinecap="round">
        <path d="M79 60h43" stroke="#ffffff" transform="rotate(-13.6 79 60)" />
        <path d="M79 60h43" stroke={onDark ? "#5fd8cb" : "#a9ece0"} />
        <path d="M79 60h43" stroke={onDark ? "#2f8f86" : "#5ec9b6"} transform="rotate(13.6 79 60)" />
      </g>
    </svg>
  );
}
