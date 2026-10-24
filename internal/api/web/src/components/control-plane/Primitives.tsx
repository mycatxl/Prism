import {
  Button as HeroButton,
  Checkbox as HeroCheckbox,
  Drawer as HeroDrawer,
  Input as HeroInput,
  Switch as HeroSwitch,
  Tabs as HeroTabs,
  useOverlayState,
} from "@heroui/react";
export function ControlTextarea({
  label,
  hint,
  className,
  ...props
}: React.TextareaHTMLAttributes<HTMLTextAreaElement> & { label?: ReactNode; hint?: ReactNode }) {
  return (
    <label className={cn("cp-field", className)}>
      {label ? <span className="cp-field__label">{label}</span> : null}
      <textarea {...props} className="cp-textarea" />
      {hint ? <span className="cp-field__hint">{hint}</span> : null}
    </label>
  );
}

export function ControlCheckbox({
  label,
  hint,
  className,
  ...props
}: React.ComponentProps<typeof HeroCheckbox> & { label?: ReactNode; hint?: ReactNode }) {
  return (
    <HeroCheckbox {...props} className={cn("cp-checkbox", className)}>
      <HeroCheckbox.Content>
        <HeroCheckbox.Control>
          <HeroCheckbox.Indicator />
        </HeroCheckbox.Control>
        {label ? <span className="cp-checkbox__label">{label}</span> : null}
      </HeroCheckbox.Content>
      {hint ? <span className="cp-checkbox__hint">{hint}</span> : null}
    </HeroCheckbox>
  );
}

export function ControlSwitch({
  label,
  hint,
  className,
  ...props
}: React.ComponentProps<typeof HeroSwitch> & { label?: ReactNode; hint?: ReactNode }) {
  return (
    <HeroSwitch {...props} className={cn("cp-switch", className)}>
      <HeroSwitch.Content>
        <HeroSwitch.Control>
          <HeroSwitch.Thumb />
        </HeroSwitch.Control>
        {label ? <span className="cp-switch__label">{label}</span> : null}
      </HeroSwitch.Content>
      {hint ? <span className="cp-switch__hint">{hint}</span> : null}
    </HeroSwitch>
  );
}

import type { ReactNode } from "react";
import { ArrowLeft, ArrowRight, X } from "lucide-react";
import { cn } from "../../lib/cn";

type Tone = "live" | "signal" | "warn" | "alert" | "neutral";

export function ControlButton({
  className,
  loading,
  children,
  ...props
}: React.ComponentProps<typeof HeroButton> & { loading?: boolean }) {
  return (
    <HeroButton
      {...props}
      className={cn("cp-button", className)}
      isDisabled={props.isDisabled || loading}
      aria-busy={loading || undefined}
    >
      {loading ? <span className="cp-spinner" aria-hidden /> : null}
      {children as ReactNode}
    </HeroButton>
  );
}

export function ControlInput({
  label,
  hint,
  className,
  ...props
}: React.ComponentProps<typeof HeroInput> & { label?: ReactNode; hint?: ReactNode }) {
  return (
    <label className={cn("cp-field", className)}>
      {label ? <span className="cp-field__label">{label}</span> : null}
      <HeroInput {...props} className="cp-input" />
      {hint ? <span className="cp-field__hint">{hint}</span> : null}
    </label>
  );
}

export function ControlSelect({
  label,
  hint,
  className,
  children,
  ...props
}: React.SelectHTMLAttributes<HTMLSelectElement> & { label?: ReactNode; hint?: ReactNode }) {
  return (
    <label className={cn("cp-field", className)}>
      {label ? <span className="cp-field__label">{label}</span> : null}
      <select {...props} className="cp-select">
        {children}
      </select>
      {hint ? <span className="cp-field__hint">{hint}</span> : null}
    </label>
  );
}

export function ControlTabs({
  items,
  selectedKey,
  onSelectionChange,
  label,
}: {
  items: Array<{ id: string; label: ReactNode; count?: ReactNode }>;
  selectedKey: string;
  onSelectionChange: (key: string) => void;
  label: string;
}) {
  return (
    <HeroTabs
      aria-label={label}
      selectedKey={selectedKey}
      onSelectionChange={(key) => onSelectionChange(String(key))}
      className="cp-tabs"
      variant="primary"
    >
      <HeroTabs.List className="cp-tabs__list">
        {items.map((item) => (
          <HeroTabs.Tab key={item.id} id={item.id} className="cp-tabs__tab">
            <span>{item.label}</span>
            {item.count !== undefined ? <span className="cp-tabs__count">{item.count}</span> : null}
          </HeroTabs.Tab>
        ))}
      </HeroTabs.List>
    </HeroTabs>
  );
}
export function ControlPagination({
  page,
  totalItems,
  pageSize,
  pageSizes = [20, 40, 80],
  disabled = false,
  labels,
  onPageChange,
  onPageSizeChange,
}: {
  page: number;
  totalItems: number;
  pageSize: number;
  pageSizes?: readonly number[];
  disabled?: boolean;
  labels: {
    summary: (values: { page: number; pages: number; start: number; end: number; total: number }) => ReactNode;
    perPage: string;
    pageOption: (size: number) => ReactNode;
    previous: string;
    next: string;
  };
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const totalPages = Math.max(1, Math.ceil(totalItems / pageSize));
  const current = Math.min(Math.max(0, page), totalPages - 1);
  const start = totalItems === 0 ? 0 : current * pageSize + 1;
  const end = Math.min((current + 1) * pageSize, totalItems);
  return (
    <div className="cp-pagination">
      <span>{labels.summary({ page: current + 1, pages: totalPages, start, end, total: totalItems })}</span>
      <div>
        <ControlSelect
          aria-label={labels.perPage}
          value={pageSize}
          disabled={disabled}
          onChange={(event) => onPageSizeChange(Number(event.target.value))}
        >
          {pageSizes.map((size) => <option key={size} value={size}>{labels.pageOption(size)}</option>)}
        </ControlSelect>
        <ControlButton
          variant="ghost"
          size="sm"
          isIconOnly
          aria-label={labels.previous}
          isDisabled={disabled || current === 0}
          onPress={() => onPageChange(current - 1)}
        >
          <ArrowLeft size={14} aria-hidden />
        </ControlButton>
        <ControlButton
          variant="ghost"
          size="sm"
          isIconOnly
          aria-label={labels.next}
          isDisabled={disabled || current >= totalPages - 1}
          onPress={() => onPageChange(current + 1)}
        >
          <ArrowRight size={14} aria-hidden />
        </ControlButton>
      </div>
    </div>
  );
}



export function StatusMark({ tone, label }: { tone: Tone; label: ReactNode }) {
  return (
    <span className={cn("cp-status", `cp-status--${tone}`)}>
      <span className="cp-status__mark" aria-hidden />
      <span>{label}</span>
    </span>
  );
}

export function QueryBadge({ label, tone = "neutral" }: { label: ReactNode; tone?: Tone }) {
  return <span className={cn("cp-query-badge", `cp-query-badge--${tone}`)}>{label}</span>;
}

export function FilterChip({ label, onRemove }: { label: ReactNode; onRemove?: () => void }) {
  return (
    <span className="cp-filter-chip">
      <span>{label}</span>
      {onRemove ? (
        <button type="button" className="cp-filter-chip__remove" onClick={onRemove} aria-label={`移除 ${String(label)}`}>
          <X size={12} aria-hidden />
        </button>
      ) : null}
    </span>
  );
}

export function CommandSurface({
  children,
  actions,
  className,
}: {
  children: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("cp-command", className)}>
      <div className="cp-command__body">{children}</div>
      {actions ? <div className="cp-command__actions">{actions}</div> : null}
    </section>
  );
}

export function WorkSurface({
  eyebrow,
  title,
  description,
  meta,
  actions,
  children,
  className,
}: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("cp-surface", className)}>
      <header className="cp-surface__head">
        <div className="cp-surface__heading">
          {eyebrow ? <span className="cp-eyebrow">{eyebrow}</span> : null}
          <div className="cp-surface__title-line">
            <h2 className="cp-surface__title">{title}</h2>
            {meta ? <span className="cp-surface__meta">{meta}</span> : null}
          </div>
          {description ? <p className="cp-surface__description">{description}</p> : null}
        </div>
        {actions ? <div className="cp-surface__actions">{actions}</div> : null}
      </header>
      <div className="cp-surface__body">{children}</div>
    </section>
  );
}

export function ObjectList({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("cp-object-list", className)}>{children}</div>;
}

export function ObjectRow({
  active,
  onClick,
  leading,
  title,
  subtitle,
  facts,
  status,
  action,
  className,
}: {
  active?: boolean;
  onClick?: () => void;
  leading?: ReactNode;
  title: ReactNode;
  subtitle?: ReactNode;
  facts?: ReactNode;
  status?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  const content = (
    <>
      <span className="cp-object-row__leading">{leading}</span>
      <span className="cp-object-row__identity">
        <strong>{title}</strong>
        {subtitle ? <small>{subtitle}</small> : null}
      </span>
      {facts ? <span className="cp-object-row__facts">{facts}</span> : null}
      {status ? <span className="cp-object-row__status">{status}</span> : null}
      {action ? <span className="cp-object-row__action">{action}</span> : null}
    </>
  );

  if (onClick) {
    return (
      <button type="button" className={cn("cp-object-row", active && "is-active", className)} onClick={onClick}>
        {content}
      </button>
    );
  }

  return <div className={cn("cp-object-row", active && "is-active", className)}>{content}</div>;
}

export function DetailFact({ label, value, mono = false }: { label: ReactNode; value: ReactNode; mono?: boolean }) {
  return (
    <div className="cp-detail-fact">
      <dt>{label}</dt>
      <dd className={mono ? "is-mono" : undefined}>{value}</dd>
    </div>
  );
}

export function InspectorDrawer({
  open,
  onOpenChange,
  title,
  eyebrow,
  children,
  footer,
  placement = "right",
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  eyebrow?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  placement?: "left" | "right" | "top" | "bottom";
}) {
  const state = useOverlayState({ isOpen: open, onOpenChange });
  return (
    <HeroDrawer state={state}>
      <HeroDrawer.Backdrop variant="blur" />
      <HeroDrawer.Content placement={placement} className="cp-drawer">
        <HeroDrawer.Dialog>
          <HeroDrawer.Header className="cp-drawer__head">
            <div>
              {eyebrow ? <span className="cp-eyebrow">{eyebrow}</span> : null}
              <HeroDrawer.Heading>{title}</HeroDrawer.Heading>
            </div>
            <HeroDrawer.CloseTrigger className="cp-icon-button" aria-label="关闭">
              <X size={16} aria-hidden />
            </HeroDrawer.CloseTrigger>
          </HeroDrawer.Header>
          <HeroDrawer.Body className="cp-drawer__body">{children}</HeroDrawer.Body>
          {footer ? <HeroDrawer.Footer className="cp-drawer__footer">{footer}</HeroDrawer.Footer> : null}
        </HeroDrawer.Dialog>
      </HeroDrawer.Content>
    </HeroDrawer>
  );
}
export function ConfirmDrawer({
  open,
  onOpenChange,
  title,
  eyebrow,
  message,
  cancelLabel,
  confirmLabel,
  loading = false,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  eyebrow?: ReactNode;
  message: ReactNode;
  cancelLabel: ReactNode;
  confirmLabel: ReactNode;
  loading?: boolean;
  onConfirm: () => void;
}) {
  return (
    <InspectorDrawer
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      eyebrow={eyebrow}
      footer={
        <>
          <ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)} isDisabled={loading}>
            {cancelLabel}
          </ControlButton>
          <ControlButton variant="danger" size="sm" onPress={onConfirm} loading={loading}>
            {confirmLabel}
          </ControlButton>
        </>
      }
    >
      <p className="cp-confirm-copy">{message}</p>
    </InspectorDrawer>
  );
}


export function EmptyWorkState({ title, detail, action }: { title: ReactNode; detail?: ReactNode; action?: ReactNode }) {
  return (
    <div className="cp-empty">
      <span className="cp-empty__mark" aria-hidden />
      <strong>{title}</strong>
      {detail ? <p>{detail}</p> : null}
      {action ? <div className="cp-empty__action">{action}</div> : null}
    </div>
  );
}

export function PageFrame({
  eyebrow,
  title,
  description,
  meta,
  actions,
  children,
}: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="cp-page">
      <header className="cp-page__head">
        <div className="cp-page__identity">
          {eyebrow ? <span className="cp-eyebrow">{eyebrow}</span> : null}
          <h1>{title}</h1>
          {description ? <p>{description}</p> : null}
        </div>
        <div className="cp-page__tools">
          {meta ? <div className="cp-page__meta">{meta}</div> : null}
          {actions ? <div className="cp-page__actions">{actions}</div> : null}
        </div>
      </header>
      <div className="cp-page__body">{children}</div>
    </div>
  );
}

export type { Tone };
