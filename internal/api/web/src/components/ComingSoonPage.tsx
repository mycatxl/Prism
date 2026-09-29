import { Panel } from "./ui/Panel";

type ComingSoonPageProps = {
  title: string;
  description: string;
};

/**
 * Placeholder for a destination that has a route but no screen yet.
 *
 * It states what will live here, so the nav entry is not a dead end that leaves
 * the reader guessing whether something is broken.
 */
export function ComingSoonPage({ title, description }: ComingSoonPageProps) {
  return (
    <div className="p-6">
      <Panel className="max-w-xl p-5">
        <h2 className="text-base font-semibold">{title}</h2>
        <p className="mt-1 text-xs leading-relaxed text-ink-soft">{description}</p>
      </Panel>
    </div>
  );
}