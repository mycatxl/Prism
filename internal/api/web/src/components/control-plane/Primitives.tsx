import * as Dialog from "@radix-ui/react-dialog";
import * as Select from "@radix-ui/react-select";
import * as Switch from "@radix-ui/react-switch";
import * as Tabs from "@radix-ui/react-tabs";
import {
  createContext,
  forwardRef,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type MouseEventHandler,
  type ReactNode,
  type TableHTMLAttributes,
  type TdHTMLAttributes,
  type TextareaHTMLAttributes,
  type ThHTMLAttributes,
  useContext,
  useId,
  useRef,
} from "react";
import { ArrowLeft, ArrowRight, Check, ChevronDown, Inbox, X } from "lucide-react";
import { cn } from "../../lib/cn";
import { Link, type LinkProps } from "react-router-dom";

type Tone = "live" | "signal" | "warn" | "alert" | "neutral";
type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
type ButtonSize = "sm" | "md" | "lg" | "icon";

const WorkspaceCardDensityContext = createContext(false);

type FieldProps = {
  label?: ReactNode;
  hint?: ReactNode;
  invalid?: boolean;
  className?: string;
};

function FieldMessage({ hint, invalid }: { hint?: ReactNode; invalid?: boolean }) {
  if (!hint) return null;
  return <span className={invalid ? "workspace-field__error" : "workspace-field__hint"}>{hint}</span>;
}

export function ControlTextarea({ label, hint, invalid, className, disabled, id, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement> & FieldProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;
  return (
    <div className={cn("workspace-field", className)}>
      {label ? <label className="workspace-field__label" htmlFor={controlId}>{label}</label> : null}
      <textarea
        {...props}
        id={controlId}
        className="workspace-textarea"
        disabled={disabled}
        aria-invalid={invalid || props["aria-invalid"] || undefined}
      />
      <FieldMessage hint={hint} invalid={invalid} />
    </div>
  );
}

type ControlCheckboxProps = Omit<InputHTMLAttributes<HTMLInputElement>, "checked" | "onChange" | "type"> & FieldProps & {
  isSelected?: boolean;
  isDisabled?: boolean;
  onChange?: (checked: boolean) => void;
};

export function ControlCheckbox({ label, hint, className, isSelected, isDisabled, disabled, onChange, ...props }: ControlCheckboxProps) {
  return (
    <label className={cn("workspace-checkbox", className)}>
      <span className="workspace-checkbox__content">
        <input
          {...props}
          type="checkbox"
          checked={Boolean(isSelected)}
          disabled={isDisabled || disabled}
          onChange={(event) => onChange?.(event.target.checked)}
        />
        <span className="workspace-checkbox__control" aria-hidden><Check size={12} /></span>
        {label ? <span className="workspace-checkbox__label">{label}</span> : null}
      </span>
      {hint ? <span className="workspace-checkbox__hint">{hint}</span> : null}
    </label>
  );
}

type ControlSwitchProps = Omit<React.ComponentPropsWithoutRef<typeof Switch.Root>, "checked" | "onCheckedChange" | "disabled" | "onChange"> & FieldProps & {
  isSelected?: boolean;
  isDisabled?: boolean;
  disabled?: boolean;
  onChange?: (checked: boolean) => void;
};

export function ControlSwitch({ label, hint, className, isSelected, isDisabled, disabled, onChange, ...props }: ControlSwitchProps) {
  return (
    <label className={cn("workspace-switch", className)}>
      <span className="workspace-switch__content">
        <Switch.Root
          {...props}
          className="workspace-switch__control"
          checked={Boolean(isSelected)}
          disabled={isDisabled || disabled}
          onCheckedChange={onChange}
        >
          <Switch.Thumb className="workspace-switch__thumb" />
        </Switch.Root>
        {label ? <span className="workspace-switch__label">{label}</span> : null}
      </span>
      {hint ? <span className="workspace-switch__hint">{hint}</span> : null}
    </label>
  );
}

type ControlButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "disabled" | "onClick"> & {
  children?: ReactNode;
  loading?: boolean;
  variant?: ButtonVariant;
  size?: ButtonSize;
  disabled?: boolean;
  isDisabled?: boolean;
  isIconOnly?: boolean;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  onPress?: MouseEventHandler<HTMLButtonElement> | (() => void);
};

export function ControlButton({
  className,
  loading,
  children,
  size = "md",
  variant = "secondary",
  disabled,
  isDisabled,
  isIconOnly,
  onClick,
  onPress,
  type = "button",
  ...props
}: ControlButtonProps) {
  const iconOnly = isIconOnly || size === "icon";
  const handleClick: MouseEventHandler<HTMLButtonElement> | undefined = onClick || onPress
    ? (event) => {
      onClick?.(event);
      if (!event.defaultPrevented) (onPress as MouseEventHandler<HTMLButtonElement> | undefined)?.(event);
    }
    : undefined;
  return (
    <button
      {...props}
      type={type}
      data-variant={variant}
      data-size={size}
      className={cn("workspace-button", iconOnly && "workspace-button--icon", className)}
      disabled={disabled || isDisabled || loading}
      aria-busy={loading || undefined}
      onClick={handleClick}
    >
      {loading ? <span className="workspace-spinner" aria-hidden /> : null}
      {children}
    </button>
  );
}

export const ControlInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & FieldProps>(function ControlInput(
  { label, hint, invalid, className, disabled, id, ...props },
  ref,
) {
  const generatedId = useId();
  const controlId = id ?? generatedId;
  return (
    <div className={cn("workspace-field", className)}>
      {label ? <label className="workspace-field__label" htmlFor={controlId}>{label}</label> : null}
      <input
        ref={ref}
        {...props}
        id={controlId}
        className="workspace-input"
        disabled={disabled}
        aria-invalid={invalid || props["aria-invalid"] || undefined}
      />
      <FieldMessage hint={hint} invalid={invalid} />
    </div>
  );
});

export type SelectOption = {
  id: string;
  label: ReactNode;
  isDisabled?: boolean;
};

const EMPTY_SELECT_VALUE = "__prism_empty__";
const toRadixValue = (value: string | null | undefined) => value === "" ? EMPTY_SELECT_VALUE : value;
const fromRadixValue = (value: string) => value === EMPTY_SELECT_VALUE ? "" : value;

type ControlSelectProps = FieldProps & {
  items: readonly SelectOption[];
  selectedKey?: string | null;
  disabled?: boolean;
  onSelectionChange?: (key: string | null) => void;
  "aria-label"?: string;
  id?: string;
  name?: string;
  required?: boolean;
};

export function ControlSelect({
  label,
  hint,
  invalid,
  className,
  items,
  selectedKey,
  disabled,
  onSelectionChange,
  "aria-label": ariaLabel,
  name,
  required,
}: ControlSelectProps) {
  const generatedId = useId();
  const value = toRadixValue(selectedKey);
  const selected = items.find((item) => item.id === (selectedKey ?? ""));
  return (
    <div className={cn("workspace-select", className)}>
      {label ? <span className="workspace-field__label" id={`${generatedId}-label`}>{label}</span> : null}
      <Select.Root
        value={value ?? undefined}
        disabled={disabled}
        name={name}
        required={required}
        onValueChange={(next) => onSelectionChange?.(fromRadixValue(next))}
      >
        <Select.Trigger
          className="workspace-select__trigger"
          aria-label={ariaLabel}
          aria-labelledby={label ? `${generatedId}-label` : undefined}
          aria-invalid={invalid || undefined}
        >
          <Select.Value>{selected?.label}</Select.Value>
          <Select.Icon><ChevronDown size={15} aria-hidden /></Select.Icon>
        </Select.Trigger>
        <Select.Portal>
          <Select.Content className="workspace-select__popover" position="popper" sideOffset={6} collisionPadding={12}>
            <Select.Viewport>
              {items.map((item) => (
                <Select.Item
                  key={item.id || EMPTY_SELECT_VALUE}
                  value={toRadixValue(item.id) ?? EMPTY_SELECT_VALUE}
                  disabled={item.isDisabled}
                  className="workspace-select__option"
                >
                  <Select.ItemText>{item.label}</Select.ItemText>
                  <Select.ItemIndicator className="workspace-select__check"><Check size={14} /></Select.ItemIndicator>
                </Select.Item>
              ))}
            </Select.Viewport>
          </Select.Content>
        </Select.Portal>
      </Select.Root>
      <FieldMessage hint={hint} invalid={invalid} />
    </div>
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
    <Tabs.Root value={selectedKey} onValueChange={onSelectionChange} className="workspace-tabs">
      <Tabs.List className="workspace-tabs__list" aria-label={label}>
        {items.map((item) => (
          <Tabs.Trigger key={item.id} value={item.id} className="workspace-tabs__tab">
            <span>{item.label}</span>
            {item.count !== undefined ? <span className="workspace-tabs__count">{item.count}</span> : null}
          </Tabs.Trigger>
        ))}
      </Tabs.List>
    </Tabs.Root>
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
    <div className="workspace-pagination">
      <span>{labels.summary({ page: current + 1, pages: totalPages, start, end, total: totalItems })}</span>
      <div className="workspace-pagination__actions">
        <ControlSelect
          aria-label={labels.perPage}
          selectedKey={String(pageSize)}
          items={pageSizes.map((size) => ({ id: String(size), label: labels.pageOption(size) }))}
          disabled={disabled}
          onSelectionChange={(key) => { if (key !== null) onPageSizeChange(Number(key)); }}
        />
        <ControlButton variant="ghost" size="icon" aria-label={labels.previous} isDisabled={disabled || current === 0} onPress={() => onPageChange(current - 1)}>
          <ArrowLeft size={16} aria-hidden />
        </ControlButton>
        <ControlButton variant="ghost" size="icon" aria-label={labels.next} isDisabled={disabled || current >= totalPages - 1} onPress={() => onPageChange(current + 1)}>
          <ArrowRight size={16} aria-hidden />
        </ControlButton>
      </div>
    </div>
  );
}

export type ControlTableDensity = "compact" | "comfortable";

export function ControlTableWrap({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div {...rest} data-scrollable="true" className={cn("workspace-table-region workspace-table__scroll", className)}>{children}</div>;
}

export function ControlTable({ className, density = "comfortable", children, ...rest }: TableHTMLAttributes<HTMLTableElement> & { density?: ControlTableDensity }) {
  return <table {...rest} className={cn("workspace-table", `workspace-table--${density}`, className)}>{children}</table>;
}

export function ControlTableHead({ className, children, ...rest }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead {...rest} className={className}><tr>{children}</tr></thead>;
}

export function ControlTableColumn({ className, children, isRowHeader: _isRowHeader, ...rest }: ThHTMLAttributes<HTMLTableCellElement> & { isRowHeader?: boolean }) {
  void _isRowHeader;
  return <th {...rest} scope="col" className={cn("workspace-table__column", className)}>{children}</th>;
}

export function ControlTableBody({ className, children, ...rest }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody {...rest} className={className}>{children}</tbody>;
}

export function ControlTableRow({ className, selected, children, onAction, onClick, ...rest }: React.HTMLAttributes<HTMLTableRowElement> & { selected?: boolean; onAction?: () => void }) {
  const activate = onClick ?? (onAction ? (() => onAction()) : undefined);
  return (
    <tr
      {...rest}
      data-selected={selected ? "true" : undefined}
      className={cn("workspace-table__row", className)}
      onClick={activate}
      onKeyDown={onAction ? (event) => {
        if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
        event.preventDefault();
        onAction();
      } : rest.onKeyDown}
      tabIndex={onAction ? 0 : rest.tabIndex}
    >
      {children}
    </tr>
  );
}

export function ControlTableCell({ className, title, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td {...rest} className={cn("workspace-table__cell", className)}>{title ? <span title={title}>{children}</span> : children}</td>;
}

export function ControlTableNumericCell({ className, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <ControlTableCell {...rest} className={cn("workspace-table__cell workspace-table__cell--end", className)}>{children}</ControlTableCell>;
}

export function ControlTableClipCell({ className, title, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <ControlTableCell {...rest} className={cn("workspace-table__cell max-w-0 truncate", className)} title={title ?? (typeof children === "string" ? children : undefined)}>{children}</ControlTableCell>;
}

export function StatusMark({ tone, label }: { tone: Tone; label: ReactNode }) {
  return <span className={cn("workspace-status", `workspace-status--${tone}`)}><span className="workspace-status__mark" aria-hidden /><span>{label}</span></span>;
}

export function QueryBadge({ label, tone = "neutral" }: { label: ReactNode; tone?: Tone }) {
  return <span className={cn("workspace-query-badge", `workspace-query-badge--${tone}`)}>{tone !== "neutral" ? <span className="workspace-badge__dot" aria-hidden /> : null}{label}</span>;
}

export function FilterChip({ label, onRemove }: { label: ReactNode; onRemove?: () => void }) {
  return (
    <span className="workspace-filter-chip">
      <span>{label}</span>
      {onRemove ? <ControlButton size="icon" variant="ghost" className="workspace-filter-chip__remove" onPress={onRemove} aria-label={`移除 ${String(label)}`}><X size={14} aria-hidden /></ControlButton> : null}
    </span>
  );
}

export function CommandSurface({ children, actions, className }: { children: ReactNode; actions?: ReactNode; className?: string }) {
  return <section className={cn("workspace-command", className)}><div className="workspace-command__body">{children}</div>{actions ? <div className="workspace-command__actions">{actions}</div> : null}</section>;
}

export function SectionHeading({ children, className, trailing }: { children: ReactNode; className?: string; trailing?: ReactNode }) {
  return <div className={cn("workspace-section-title", className)}><h2>{children}</h2>{trailing}</div>;
}

export function WorkspaceCard({ title, description, meta, actions, children, className, headerless = false, showDescription = false }: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  headerless?: boolean;
  showDescription?: boolean;
}) {
  const conciseCards = useContext(WorkspaceCardDensityContext);
  const preserveDescription = showDescription || className?.split(/\s+/).includes("workspace-card--explainer");
  const shouldShowDescription = Boolean(description && (!conciseCards || preserveDescription));
  const hasToolbar = Boolean(meta || actions);
  return (
    <section className={cn("workspace-card", headerless && "workspace-card--headerless", className)}>
      {headerless ? (
        hasToolbar ? <div className="workspace-card__toolbar">{meta ? <span className="workspace-card__meta">{meta}</span> : null}{actions ? <div className="workspace-card__actions">{actions}</div> : null}</div> : null
      ) : (
        <header className="workspace-card__head">
          <div className="workspace-card__heading">
            <div className="workspace-card__title-line"><h2 className="workspace-card__title">{title}</h2>{meta ? <span className="workspace-card__meta">{meta}</span> : null}</div>
            {shouldShowDescription ? <p className="workspace-card__description">{description}</p> : null}
          </div>
          {actions ? <div className="workspace-card__actions">{actions}</div> : null}
        </header>
      )}
      <div className="workspace-card__body">{children}</div>
    </section>
  );
}

export function ObjectList({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("workspace-object-list", className)}>{children}</div>;
}

export function ObjectRow({ active, onClick, to, state, leading, title, subtitle, facts, status, action, className }: {
  active?: boolean;
  onClick?: () => void;
  to?: LinkProps["to"];
  state?: LinkProps["state"];
  leading?: ReactNode;
  title: ReactNode;
  subtitle?: ReactNode;
  facts?: ReactNode;
  status?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  const content = <>
    <span className="workspace-object-row__leading">{leading}</span>
    <span className="workspace-object-row__identity"><strong>{title}</strong>{subtitle ? <small>{subtitle}</small> : null}</span>
    {facts ? <span className="workspace-object-row__facts">{facts}</span> : null}
    {status ? <span className="workspace-object-row__status">{status}</span> : null}
    {action ? <span className="workspace-object-row__action" onClick={to ? undefined : (event) => event.stopPropagation()}>{action}</span> : null}
  </>;
  if (to) return <Link to={to} state={state} className={cn("workspace-object-row is-clickable", active && "is-active", className)}>{content}</Link>;
  return (
    <div
      className={cn("workspace-object-row", active && "is-active", onClick && "is-clickable", className)}
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick}
      onKeyDown={onClick ? (event) => {
        if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
        event.preventDefault();
        onClick();
      } : undefined}
    >
      {content}
    </div>
  );
}

export function DetailFact({ label, value, mono = false }: { label: ReactNode; value: ReactNode; mono?: boolean }) {
  return <div className="workspace-detail-fact"><dt>{label}</dt><dd className={mono ? "is-mono" : undefined}>{value}</dd></div>;
}

export function InspectorDrawer({ open, onOpenChange, title, eyebrow, children, footer, width = "lg", dismissible = true }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  eyebrow?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: "sm" | "md" | "lg";
  dismissible?: boolean;
}) {
  const context = typeof eyebrow === "string" ? null : eyebrow;
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  return (
    <Dialog.Root open={open} onOpenChange={(next) => { if (next || dismissible) onOpenChange(next); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="workspace-drawer-overlay" />
        <Dialog.Content
          className={cn("workspace-drawer", `workspace-dialog--${width}`)}
          data-placement="center"
          aria-describedby={undefined}
          onEscapeKeyDown={(event) => { if (!dismissible) event.preventDefault(); }}
          onPointerDownOutside={(event) => { if (!dismissible) event.preventDefault(); }}
          onOpenAutoFocus={() => {
            const active = document.activeElement;
            restoreFocusRef.current = active instanceof HTMLElement && active !== document.body ? active : null;
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            const target = restoreFocusRef.current;
            restoreFocusRef.current = null;
            if (!target?.isConnected || target.hasAttribute("disabled")) return;
            requestAnimationFrame(() => target.focus({ preventScroll: true }));
          }}
        >
          <header className="workspace-drawer__head">
            <div className="workspace-drawer__identity"><Dialog.Title>{title}</Dialog.Title>{context ? <div className="workspace-drawer__context">{context}</div> : null}</div>
            <Dialog.Close className="workspace-icon-button" aria-label="关闭" disabled={!dismissible}><X size={17} aria-hidden /></Dialog.Close>
          </header>
          <div className="workspace-drawer__body">{children}</div>
          {footer ? <footer className="workspace-drawer__footer">{footer}</footer> : null}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function ConfirmDrawer({ open, onOpenChange, title, eyebrow, message, cancelLabel, confirmLabel, loading = false, onConfirm }: {
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
    <InspectorDrawer open={open} onOpenChange={onOpenChange} width="sm" dismissible={!loading} title={title} eyebrow={eyebrow} footer={<><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)} isDisabled={loading}>{cancelLabel}</ControlButton><ControlButton variant="danger" size="sm" onPress={onConfirm} loading={loading}>{confirmLabel}</ControlButton></>}>
      <p className="workspace-confirm-copy">{message}</p>
    </InspectorDrawer>
  );
}

export function EmptyWorkState({ title, detail, action, icon, embedded = false, className }: {
  title: ReactNode;
  detail?: ReactNode;
  action?: ReactNode;
  icon?: ReactNode;
  embedded?: boolean;
  className?: string;
}) {
  return (
    <div className={cn("workspace-empty", embedded && "workspace-empty--embedded", className)}>
      <div className="workspace-empty__icon" aria-hidden>
        {icon ?? <Inbox size={20} />}
      </div>
      <strong className="workspace-empty__title">{title}</strong>
      {detail ? <p className="workspace-empty__description">{detail}</p> : null}
      {action ? <div className="workspace-empty__action">{action}</div> : null}
    </div>
  );
}

function inferWorkspaceLayout(eyebrow: ReactNode): "standard" | "overview" | "list" | "editor" | "billing" | "detail" {
  if (typeof eyebrow !== "string") return "standard";
  const context = eyebrow.toLowerCase();
  if (context.includes("overview") || context.includes("runtime")) return "overview";
  if (context.includes("billing") || context.includes("usage") || context.includes("deposit") || context.includes("plans")) return "billing";
  if (context.includes("detail") || context.includes("node") || context.includes("platform")) return "detail";
  if (context.includes("policy") || context.includes("config") || context.includes("security") || context.includes("rule") || context.includes("endpoint")) return "editor";
  if (context.includes("inventory") || context.includes("delivery") || context.includes("customers") || context.includes("governance")) return "list";
  return "standard";
}

export function WorkspacePage({ eyebrow, title, description, meta, actions, navigation, children, layout, conciseCards = false }: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  navigation?: ReactNode;
  children: ReactNode;
  layout?: "standard" | "overview" | "list" | "editor" | "billing" | "detail";
  conciseCards?: boolean;
}) {
  return (
    <WorkspaceCardDensityContext.Provider value={conciseCards}>
      <div className={cn("workspace-page workspace-page--flow", `workspace-page--${layout ?? inferWorkspaceLayout(eyebrow)}`, conciseCards && "workspace-page--concise-cards")}>
        <header className="workspace-page__head">
          {navigation ? <div className="workspace-page__navigation">{navigation}</div> : null}
          <div className="workspace-page__head-main">
            <div className="workspace-page__title-row">
              <div className="workspace-page__identity"><h1>{title}</h1>{description ? <p>{description}</p> : null}</div>
              {meta || actions ? <div className="workspace-page__tools">{meta ? <div className="workspace-page__meta">{meta}</div> : null}{actions ? <div className="workspace-page__actions">{actions}</div> : null}</div> : null}
            </div>
          </div>
        </header>
        <div className="workspace-page__body">{children}</div>
      </div>
    </WorkspaceCardDensityContext.Provider>
  );
}

export type { Tone };
