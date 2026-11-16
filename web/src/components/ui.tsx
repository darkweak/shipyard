import clsx from 'clsx'
import { Loader2, Plus, Trash2 } from 'lucide-react'
import { useEffect, useRef, type ButtonHTMLAttributes, type ComponentProps, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import type { DeploymentStatus } from '@/lib/api'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'

export function Button({
  variant = 'secondary',
  size = 'md',
  loading,
  icon,
  className,
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: 'sm' | 'md'; loading?: boolean; icon?: ReactNode }) {
  return (
    <button
      className={clsx(
        'inline-flex items-center justify-center gap-1.5 rounded-lg font-medium transition-colors',
        'disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent',
        size === 'sm' ? 'h-8 px-2.5 text-xs' : 'h-9 px-3.5 text-sm',
        variant === 'primary' && 'bg-accent text-accent-fg hover:brightness-110',
        variant === 'secondary' && 'border border-border bg-panel hover:bg-subtle',
        variant === 'ghost' && 'text-muted hover:bg-subtle hover:text-fg',
        variant === 'danger' && 'border border-red-500/30 text-red-600 hover:bg-red-500/10 dark:text-red-400',
        className,
      )}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 className="size-4 animate-spin" /> : icon}
      {children}
    </button>
  )
}

export function Card({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={clsx('rounded-xl border border-border bg-panel', className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
          <h2 className="text-sm font-semibold">{title}</h2>
          <div className="flex items-center gap-2">{actions}</div>
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

const control =
  'w-full rounded-lg border border-border bg-panel px-3 text-sm outline-none transition-colors placeholder:text-muted/70 focus:border-accent focus:ring-2 focus:ring-accent/20 disabled:opacity-60'

export function Input({ className, ...rest }: ComponentProps<"input">) {
  return <input className={clsx(control, 'h-9', className)} {...rest} />
}

export function Textarea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className={clsx(control, 'py-2', className)} {...rest} />
}

/** Monospace editor: Tab inserts spaces instead of moving focus. */
export function CodeArea({ className, onChange, ...rest }: Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, 'onChange'> & { onChange: (v: string) => void }) {
  return (
    <textarea
      spellCheck={false}
      className={clsx(control, 'py-2 font-mono text-[13px] leading-relaxed', className)}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => {
        if (e.key !== 'Tab' || e.shiftKey) return
        e.preventDefault()
        const el = e.currentTarget
        const { selectionStart: s, selectionEnd: end, value } = el
        onChange(value.slice(0, s) + '    ' + value.slice(end))
        requestAnimationFrame(() => el.setSelectionRange(s + 4, s + 4))
      }}
      {...rest}
    />
  )
}

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={clsx(control, 'h-9', className)} {...rest}>
      {children}
    </select>
  )
}

export function Field({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={clsx('block space-y-1.5', className)}>
      <span className="text-sm font-medium">{label}</span>
      {children}
      {hint && <span className="block text-xs text-muted">{hint}</span>}
    </label>
  )
}

const statusStyles: Record<DeploymentStatus, string> = {
  queued: 'bg-zinc-500/10 text-zinc-600 dark:text-zinc-400',
  cloning: 'bg-sky-500/10 text-sky-600 dark:text-sky-400',
  building: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
  deploying: 'bg-violet-500/10 text-violet-600 dark:text-violet-400',
  succeeded: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  failed: 'bg-red-500/10 text-red-600 dark:text-red-400',
  cancelled: 'bg-zinc-500/10 text-zinc-500',
}

export function StatusBadge({ status }: { status: DeploymentStatus }) {
  const active = ['queued', 'cloning', 'building', 'deploying'].includes(status)
  return (
    <span className={clsx('inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium', statusStyles[status])}>
      {active ? <Loader2 className="size-3 animate-spin" /> : <span className="size-1.5 rounded-full bg-current" />}
      {status}
    </span>
  )
}

export function Dot({ tone }: { tone: 'green' | 'red' | 'amber' | 'gray' }) {
  return (
    <span
      className={clsx(
        'inline-block size-2 rounded-full',
        tone === 'green' && 'bg-emerald-500 shadow-[0_0_0_3px] shadow-emerald-500/20',
        tone === 'red' && 'bg-red-500',
        tone === 'amber' && 'bg-amber-500',
        tone === 'gray' && 'bg-zinc-400',
      )}
    />
  )
}

export function ErrorBox({ children }: { children: ReactNode }) {
  if (!children) return null
  return <div className="rounded-lg border border-red-500/30 bg-red-500/5 px-3 py-2 text-sm text-red-600 dark:text-red-400">{children}</div>
}

export function Spinner() {
  return (
    <div className="flex justify-center py-16 text-muted">
      <Loader2 className="size-5 animate-spin" />
    </div>
  )
}

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0">
        <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <div className="mt-1 text-sm text-muted">{subtitle}</div>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

export function Empty({ icon, title, children }: { icon: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center rounded-xl border border-dashed border-border px-6 py-14 text-center">
      <div className="mb-3 text-muted">{icon}</div>
      <p className="font-medium">{title}</p>
      {children && <div className="mt-1 text-sm text-muted">{children}</div>}
    </div>
  )
}

/** Modal confirmation built on <dialog>: Escape and the backdrop cancel. */
export function Dialog({
  open,
  title,
  icon,
  children,
  confirmLabel,
  confirmVariant = 'primary',
  onConfirm,
  onCancel,
}: {
  open: boolean
  title: ReactNode
  icon?: ReactNode
  children: ReactNode
  confirmLabel: string
  confirmVariant?: Variant
  onConfirm: () => void
  onCancel: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (open && !d.open) d.showModal()
    if (!open && d.open) d.close()
  }, [open])
  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault()
        onCancel()
      }}
      onClick={(e) => e.target === e.currentTarget && onCancel()}
      className="m-auto w-[calc(100%-2rem)] max-w-md rounded-xl border border-border bg-panel p-0 text-fg shadow-xl backdrop:bg-black/40"
    >
      <div className="p-5">
        <h2 className="mb-2 flex items-center gap-2 text-base font-semibold">
          {icon}
          {title}
        </h2>
        <div className="space-y-2 text-sm text-muted">{children}</div>
      </div>
      <div className="flex justify-end gap-2 border-t border-border px-5 py-3">
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="button" variant={confirmVariant} onClick={onConfirm} autoFocus>
          {confirmLabel}
        </Button>
      </div>
    </dialog>
  )
}

/** Edits a string map as rows; keeps row order and allows blank rows while typing. */
export function KeyValueEditor({
  rows,
  onChange,
  keyPlaceholder = 'KEY',
  valuePlaceholder = 'value',
  secretValues,
}: {
  rows: [string, string][]
  onChange: (rows: [string, string][]) => void
  keyPlaceholder?: string
  valuePlaceholder?: string
  secretValues?: boolean
}) {
  const set = (i: number, k: string, v: string) => onChange(rows.map((r, j) => (j === i ? [k, v] : r)))
  return (
    <div className="space-y-2">
      {rows.map(([k, v], i) => (
        <div key={i} className="flex gap-2">
          <Input className="font-mono text-xs" placeholder={keyPlaceholder} value={k} onChange={(e) => set(i, e.target.value, v)} />
          <Input
            className="font-mono text-xs"
            placeholder={valuePlaceholder}
            type={secretValues ? 'password' : 'text'}
            autoComplete="off"
            value={v}
            onChange={(e) => set(i, k, e.target.value)}
          />
          <Button type="button" variant="ghost" className="shrink-0 px-2" onClick={() => onChange(rows.filter((_, j) => j !== i))} aria-label="Remove">
            <Trash2 className="size-4" />
          </Button>
        </div>
      ))}
      <Button type="button" size="sm" icon={<Plus className="size-3.5" />} onClick={() => onChange([...rows, ['', '']])}>
        Add
      </Button>
    </div>
  )
}

export const toRows = (m: Record<string, string> | undefined): [string, string][] => Object.entries(m ?? {})
export const fromRows = (rows: [string, string][]) =>
  Object.fromEntries(rows.filter(([k]) => k.trim() !== '').map(([k, v]) => [k.trim(), v]))
