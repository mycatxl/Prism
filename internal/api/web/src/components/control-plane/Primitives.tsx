import {
  createContext,
  forwardRef,
  type ComponentProps,
  type HTMLAttributes,
  type ReactNode,
  useContext,
} from "react";
import {
  Button as HeroButton,
  Card as HeroCard,
  Checkbox as HeroCheckbox,
  Description as HeroDescription,
  Drawer as HeroDrawer,
  FieldError as HeroFieldError,
  Input as HeroInput,
  Label as HeroLabel,
  ListBox as HeroListBox,
  Select as HeroSelect,
  Surface as HeroSurface,
  Switch as HeroSwitch,
  Table as HeroTable,
  Tabs as HeroTabs,
  TextArea as HeroTextArea,
  TextField as HeroTextField,
  useOverlayState,
} from "@heroui/react";
import type { SelectRootProps } from "@heroui/react";
import { ArrowLeft, ArrowRight, X } from "lucide-react";
import { cn } from "../../lib/cn";

type Tone = "live" | "signal" | "warn" | "alert" | "neutral";

const WorkspaceCardDensityContext = createContext(false);

type FieldProps = {
  label?: ReactNode;
  hint?: ReactNode;
  invalid?: boolean;
  className?: string;
};

export function ControlTextarea({ label, hint, invalid, className, disabled, ...props }: React.TextareaHTMLAttributes<HTMLTextAreaElement> & FieldProps) {
  return (
    <HeroTextField className={cn("workspace-field", className)} isInvalid={invalid} isDisabled={disabled}>
      {label ? <HeroLabel>{label}</HeroLabel> : null}
      <HeroTextArea {...props} className="workspace-textarea" disabled={disabled} />
      {hint ? <HeroDescription>{hint}</HeroDescription> : null}
      {invalid ? <HeroFieldError>{typeof hint === "string" ? hint : undefined}</HeroFieldError> : null}
    </HeroTextField>
  );
}

export function ControlCheckbox({ label, hint, className, ...props }: ComponentProps<typeof HeroCheckbox> & FieldProps) {
  return (
    <HeroCheckbox {...props} className={cn("workspace-checkbox", className)}>
      <HeroCheckbox.Content>
        <HeroCheckbox.Control>
          <HeroCheckbox.Indicator />
        </HeroCheckbox.Control>
        {label ? <span className="workspace-checkbox__label">{label}</span> : null}
      </HeroCheckbox.Content>
      {hint ? <span className="workspace-checkbox__hint">{hint}</span> : null}
    </HeroCheckbox>
  );
}

export function ControlSwitch({ label, hint, className, ...props }: ComponentProps<typeof HeroSwitch> & FieldProps) {
  return (
    <HeroSwitch {...props} className={cn("workspace-switch", className)}>
      <HeroSwitch.Content>
        <HeroSwitch.Control>
          <HeroSwitch.Thumb />
        </HeroSwitch.Control>
        {label ? <span className="workspace-switch__label">{label}</span> : null}
      </HeroSwitch.Content>
      {hint ? <span className="workspace-switch__hint">{hint}</span> : null}
    </HeroSwitch>
  );
}

type ControlButtonProps = Omit<ComponentProps<typeof HeroButton>, "children" | "size"> & {
  children?: ReactNode;
  loading?: boolean;
  size?: ComponentProps<typeof HeroButton>["size"] | "icon";
};

export function ControlButton({ className, loading, children, size, ...props }: ControlButtonProps) {
  return (
    <HeroButton
      {...props}
      size={size === "icon" ? "sm" : size}
      className={cn("workspace-button", size === "icon" && "workspace-button--icon", className)}
      isDisabled={props.isDisabled || loading}
      aria-busy={loading || undefined}
    >
      {loading ? <span className="workspace-spinner" aria-hidden /> : null}
      {children}
    </HeroButton>
  );
}


export const ControlInput = forwardRef<HTMLInputElement, ComponentProps<typeof HeroInput> & FieldProps & { disabled?: boolean }>(function ControlInput({ label, hint, invalid, className, disabled, ...props }, ref) {
  return (
    <HeroTextField className={cn("workspace-field", className)} isInvalid={invalid || props["aria-invalid"] === true} isDisabled={disabled}>
      {label ? <HeroLabel>{label}</HeroLabel> : null}
      <HeroInput ref={ref} {...props} className="workspace-input" />
      {hint ? <HeroDescription>{hint}</HeroDescription> : null}
      {invalid ? <HeroFieldError>{typeof hint === "string" ? hint : undefined}</HeroFieldError> : null}
    </HeroTextField>
  );
});

export type SelectOption = {
  id: string;
  label: ReactNode;
  isDisabled?: boolean;
};

function createSelectCollection(items: readonly SelectOption[]): Generator<SelectOption, "single", unknown> {
  function* selectCollection(): Generator<SelectOption, "single", unknown> {
    yield* items;
    return "single";
  }

  return selectCollection();
}

type ControlSelectProps = Omit<SelectRootProps<SelectOption, "single">, "children" | "items" | "selectedKey" | "onSelectionChange" | "isDisabled" | "isInvalid" | "className"> & FieldProps & {
  items: readonly SelectOption[];
  selectedKey?: string | null;
  disabled?: boolean;
  onSelectionChange?: (key: string | null) => void;
};

export function ControlSelect({ label, hint, invalid, className, items, selectedKey, disabled, onSelectionChange, ...props }: ControlSelectProps) {
  return (
    <HeroSelect<SelectOption, "single">
      {...props}
      items={createSelectCollection(items)}
      className={cn("workspace-select", className)}
      selectedKey={selectedKey ?? null}
      isDisabled={disabled}
      isInvalid={invalid}
      onSelectionChange={(key) => onSelectionChange?.(key == null ? null : String(key))}
    >
      {label ? <HeroLabel>{label}</HeroLabel> : null}
      <HeroSelect.Trigger className="workspace-select__trigger">
        <HeroSelect.Value />
        <HeroSelect.Indicator />
      </HeroSelect.Trigger>
      <HeroSelect.Popover className="workspace-select__popover">
        <HeroListBox>
          {items.map((item) => (
            <HeroListBox.Item key={item.id} id={item.id} isDisabled={item.isDisabled}>
              {item.label}
              <HeroListBox.ItemIndicator />
            </HeroListBox.Item>
          ))}
        </HeroListBox>
      </HeroSelect.Popover>
      {hint ? <HeroDescription>{hint}</HeroDescription> : null}
    </HeroSelect>
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
      className="workspace-tabs"
      variant="primary"
    >
      <HeroTabs.List className="workspace-tabs__list">
        {items.map((item) => (
          <HeroTabs.Tab key={item.id} id={item.id} className="workspace-tabs__tab">
            <span>{item.label}</span>
            {item.count !== undefined ? <span className="workspace-tabs__count">{item.count}</span> : null}
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
    <div className="workspace-pagination">
      <span>{labels.summary({ page: current + 1, pages: totalPages, start, end, total: totalItems })}</span>
      <div className="workspace-pagination__actions">
        <ControlSelect
          aria-label={labels.perPage}
          selectedKey={String(pageSize)}
          items={pageSizes.map((size) => ({ id: String(size), label: labels.pageOption(size) }))}
          disabled={disabled}
          onSelectionChange={(key) => {
            if (key !== null) onPageSizeChange(Number(key));
          }}
        />
        <ControlButton
          variant="ghost"
          size="icon"
          aria-label={labels.previous}
          isDisabled={disabled || current === 0}
          onPress={() => onPageChange(current - 1)}
        >
          <ArrowLeft size={16} aria-hidden />
        </ControlButton>
        <ControlButton
          variant="ghost"
          size="icon"
          aria-label={labels.next}
          isDisabled={disabled || current >= totalPages - 1}
          onPress={() => onPageChange(current + 1)}
        >
          <ArrowRight size={16} aria-hidden />
        </ControlButton>
      </div>
    </div>
  );
}

export type ControlTableDensity = "compact" | "comfortable";

type ControlTableRootProps = Omit<ComponentProps<typeof HeroTable>, "children" | "className"> & {
  className?: string;
  density?: ControlTableDensity;
  children?: ReactNode;
};

export function ControlTableWrap({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <HeroSurface {...rest} variant="secondary" className={cn("workspace-table-region workspace-table__scroll", className)}>{children}</HeroSurface>;
}

export function ControlTable({ className, density = "compact", children, ...rest }: ControlTableRootProps) {
  return (
    <HeroTable
      {...rest}
      variant="primary"
      className={cn("workspace-table", density === "compact" && "workspace-table--compact", density === "comfortable" && "workspace-table--comfortable", className)}
    >
      <HeroTable.Content>{children}</HeroTable.Content>
    </HeroTable>
  );
}

type ControlTableHeaderProps = Omit<ComponentProps<typeof HeroTable.Header>, "children" | "className"> & { className?: string; children?: ReactNode };
type ControlTableColumnProps = Omit<ComponentProps<typeof HeroTable.Column>, "children" | "className"> & { className?: string; children?: ReactNode };
type ControlTableBodyProps = Omit<ComponentProps<typeof HeroTable.Body>, "children" | "className"> & { className?: string; children?: ReactNode };
type ControlTableRowProps = Omit<ComponentProps<typeof HeroTable.Row>, "children" | "className"> & { className?: string; children?: ReactNode; selected?: boolean };
type ControlTableCellProps = Omit<ComponentProps<typeof HeroTable.Cell>, "children" | "className"> & { className?: string; children?: ReactNode; title?: string };

export function ControlTableHead({ className, children, ...rest }: ControlTableHeaderProps) {
  return <HeroTable.Header {...rest} className={className}>{children}</HeroTable.Header>;
}

export function ControlTableColumn({ className, children, ...rest }: ControlTableColumnProps) {
  return <HeroTable.Column {...rest} className={cn("workspace-table__column", className)}>{children}</HeroTable.Column>;
}

export function ControlTableBody({ className, children, ...rest }: ControlTableBodyProps) {
  return <HeroTable.Body {...rest} className={className}>{children}</HeroTable.Body>;
}

export function ControlTableRow({ className, selected, children, ...rest }: ControlTableRowProps) {
  return <HeroTable.Row {...rest} data-selected={selected ? "true" : undefined} className={cn("workspace-table__row", className)}>{children}</HeroTable.Row>;
}

export function ControlTableCell({ className, title, children, ...rest }: ControlTableCellProps) {
  return <HeroTable.Cell {...rest} className={cn("workspace-table__cell", className)}>{title ? <span title={title}>{children}</span> : children}</HeroTable.Cell>;
}

export function ControlTableNumericCell({ className, children, ...rest }: ControlTableCellProps) {
  return <ControlTableCell {...rest} className={cn("workspace-table__cell workspace-table__cell--end", className)}>{children}</ControlTableCell>;
}

export function ControlTableClipCell({ className, title, children, ...rest }: ControlTableCellProps) {
  return <ControlTableCell {...rest} className={cn("workspace-table__cell max-w-0 truncate", className)} title={title ?? (typeof children === "string" ? children : undefined)}>{children}</ControlTableCell>;
}
export function StatusMark({ tone, label }: { tone: Tone; label: ReactNode }) {
  return (
    <span className={cn("workspace-status", `workspace-status--${tone}`)}>
      <span className="workspace-status__mark" aria-hidden />
      <span>{label}</span>
    </span>
  );

}

export function QueryBadge({ label, tone = "neutral" }: { label: ReactNode; tone?: Tone }) {
  return <span className={cn("workspace-query-badge", `workspace-query-badge--${tone}`)}>{label}</span>;
}

export function FilterChip({ label, onRemove }: { label: ReactNode; onRemove?: () => void }) {
  return (
    <span className="workspace-filter-chip">
      <span>{label}</span>
      {onRemove ? (
        <HeroButton size="sm" isIconOnly variant="ghost" className="workspace-filter-chip__remove" onPress={onRemove} aria-label={`移除 ${String(label)}`}>
          <X size={14} aria-hidden />
        </HeroButton>
      ) : null}
    </span>
  );
}

export function CommandSurface({ children, actions, className }: { children: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <HeroSurface className={cn("workspace-command", className)} variant="secondary">
      <div className="workspace-command__body">{children}</div>
      {actions ? <div className="workspace-command__actions">{actions}</div> : null}
    </HeroSurface>
  );
}

export function SectionHeading({ children, className, trailing }: { children: ReactNode; className?: string; trailing?: ReactNode }) {
  return (
    <div className={cn("workspace-section-title", className)}>
      <h2>{children}</h2>
      {trailing}
    </div>
  );
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
    <HeroCard className={cn("workspace-card", headerless && "workspace-card--headerless", className)} variant="default">
      {headerless ? (
        hasToolbar ? (
          <div className="workspace-card__toolbar">
            {meta ? <span className="workspace-card__meta">{meta}</span> : null}
            {actions ? <div className="workspace-card__actions">{actions}</div> : null}
          </div>
        ) : null
      ) : (
        <HeroCard.Header className="workspace-card__head">
          <div className="workspace-card__heading">
            <div className="workspace-card__title-line">
              <HeroCard.Title className="workspace-card__title">{title}</HeroCard.Title>
              {meta ? <span className="workspace-card__meta">{meta}</span> : null}
            </div>
            {shouldShowDescription ? <HeroCard.Description className="workspace-card__description">{description}</HeroCard.Description> : null}
          </div>
          {actions ? <div className="workspace-card__actions">{actions}</div> : null}
        </HeroCard.Header>
      )}
      <HeroCard.Content className="workspace-card__body">{children}</HeroCard.Content>
    </HeroCard>
  );
}

export function ObjectList({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("workspace-object-list", className)}>{children}</div>;
}

export function ObjectRow({ active, onClick, leading, title, subtitle, facts, status, action, className }: {
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
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (!onClick || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    onClick();
  };
  return (
    <HeroSurface
      className={cn("workspace-object-row", active && "is-active", onClick && "is-clickable", className)}
      variant={active ? "tertiary" : "default"}
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick}
      onKeyDown={handleKeyDown}
    >
      <span className="workspace-object-row__leading">{leading}</span>
      <span className="workspace-object-row__identity">
        <strong>{title}</strong>
        {subtitle ? <small>{subtitle}</small> : null}
      </span>
      {facts ? <span className="workspace-object-row__facts">{facts}</span> : null}
      {status ? <span className="workspace-object-row__status">{status}</span> : null}
      {action ? <span className="workspace-object-row__action" onClick={(event) => event.stopPropagation()}>{action}</span> : null}
    </HeroSurface>
  );
}

export function DetailFact({ label, value, mono = false }: { label: ReactNode; value: ReactNode; mono?: boolean }) {
  return (
    <div className="workspace-detail-fact">
      <dt>{label}</dt>
      <dd className={mono ? "is-mono" : undefined}>{value}</dd>
    </div>
  );
}

export function InspectorDrawer({ open, onOpenChange, title, eyebrow, children, footer, placement = "right" }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  eyebrow?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  placement?: "left" | "right" | "top" | "bottom";
}) {
  const state = useOverlayState({ isOpen: open, onOpenChange });
  const context = typeof eyebrow === "string" ? null : eyebrow;
  return (
    <HeroDrawer state={state}>
      <HeroDrawer.Backdrop variant="blur">
        <HeroDrawer.Content placement={placement} className="workspace-drawer">
          <HeroDrawer.Dialog>
            <HeroDrawer.Header className="workspace-drawer__head">
              <div className="workspace-drawer__identity">
                <HeroDrawer.Heading>{title}</HeroDrawer.Heading>
                {context ? <div className="workspace-drawer__context">{context}</div> : null}
              </div>
              <HeroDrawer.CloseTrigger className="workspace-icon-button" aria-label="关闭">
                <X size={17} aria-hidden />
              </HeroDrawer.CloseTrigger>
            </HeroDrawer.Header>
            <HeroDrawer.Body className="workspace-drawer__body">{children}</HeroDrawer.Body>
            {footer ? <HeroDrawer.Footer className="workspace-drawer__footer">{footer}</HeroDrawer.Footer> : null}
          </HeroDrawer.Dialog>
        </HeroDrawer.Content>
      </HeroDrawer.Backdrop>
    </HeroDrawer>
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
    <InspectorDrawer
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      eyebrow={eyebrow}
      footer={
        <>
          <ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)} isDisabled={loading}>{cancelLabel}</ControlButton>
          <ControlButton variant="danger" size="sm" onPress={onConfirm} loading={loading}>{confirmLabel}</ControlButton>
        </>
      }
    >
      <p className="workspace-confirm-copy">{message}</p>
    </InspectorDrawer>
  );
}

export function EmptyWorkState({ title, detail, action }: { title: ReactNode; detail?: ReactNode; action?: ReactNode }) {
  return (
    <HeroCard className="workspace-empty" variant="secondary">
      <HeroCard.Content>
        <span className="workspace-empty__mark" aria-hidden />
        <HeroCard.Title>{title}</HeroCard.Title>
        {detail ? <HeroCard.Description>{detail}</HeroCard.Description> : null}
        {action ? <div className="workspace-empty__action">{action}</div> : null}
      </HeroCard.Content>
    </HeroCard>
  );
}

function inferWorkspaceLayout(eyebrow: ReactNode): "standard" | "overview" | "list" | "editor" | "billing" | "detail" { if (typeof eyebrow !== "string") return "standard"; const context = eyebrow.toLowerCase(); if (context.includes("overview") || context.includes("runtime")) return "overview"; if (context.includes("billing") || context.includes("usage") || context.includes("deposit") || context.includes("plans")) return "billing"; if (context.includes("detail") || context.includes("node") || context.includes("platform")) return "detail"; if (context.includes("policy") || context.includes("config") || context.includes("security") || context.includes("rule") || context.includes("endpoint")) return "editor"; if (context.includes("inventory") || context.includes("delivery") || context.includes("customers") || context.includes("governance")) return "list"; return "standard"; }

export function WorkspacePage({ eyebrow, title, description, meta, actions, children, layout, conciseCards = false }: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  layout?: "standard" | "overview" | "list" | "editor" | "billing" | "detail";
  conciseCards?: boolean;
}) {
  return (
    <WorkspaceCardDensityContext.Provider value={conciseCards}>
      <div className={cn(
        "workspace-page workspace-page--flow",
        `workspace-page--${layout ?? inferWorkspaceLayout(eyebrow)}`,
        conciseCards && "workspace-page--concise-cards",
      )}>
        <header className="workspace-page__head">
          <div className="workspace-page__head-main">
            <div className="workspace-page__title-row">
              <div className="workspace-page__identity">
                <h1>{title}</h1>
                {description ? <p>{description}</p> : null}
              </div>
              {meta || actions ? (
                <div className="workspace-page__tools">
                  {meta ? <div className="workspace-page__meta">{meta}</div> : null}
                  {actions ? <div className="workspace-page__actions">{actions}</div> : null}
                </div>
              ) : null}
            </div>
          </div>
        </header>
        <div className="workspace-page__body">{children}</div>
      </div>
    </WorkspaceCardDensityContext.Provider>
  );
}

export type { Tone };
