import { useId, type ReactNode } from 'react'
import { Check } from 'lucide-react'
import { Input } from '../../ui/components/input'
import { Checkbox } from '../../ui/components/checkbox'
import { cn } from '../../lib'
import type { JSONSchema } from '../../api'

// An elicitation form, as MCP defines it: a flat object whose properties are strings (plain, or
// email, uri, date and date-time), numbers and integers, booleans, single choices (enum, oneOf with
// const and title, or enum with the older enumNames) and multiple choices (an array whose items are
// an enum or anyOf with const and title). Every field may carry a title, description and default.

export type Values = Record<string, unknown>
export type Errors = Record<string, string>

interface Option {
  value: string
  label: string
}

type Field =
  | { kind: 'text'; format?: 'email' | 'uri' | 'date' | 'date-time' }
  | { kind: 'number'; integer: boolean }
  | { kind: 'boolean' }
  | { kind: 'choice'; options: Option[] }
  | { kind: 'multi'; options: Option[] }
  | { kind: 'unknown' }

const typeOf = (s: JSONSchema) => (Array.isArray(s.type) ? s.type.find((t) => t !== 'null') : s.type)

function titled(list: unknown): Option[] | undefined {
  if (!Array.isArray(list)) return undefined
  const out = list.filter((o): o is { const: unknown; title?: string } => !!o && typeof o === 'object' && 'const' in o)
  return out.length ? out.map((o) => ({ value: String(o.const), label: o.title ?? String(o.const) })) : undefined
}

function enumOptions(s: JSONSchema): Option[] | undefined {
  if (!Array.isArray(s.enum)) return undefined
  const names = Array.isArray(s.enumNames) ? (s.enumNames as unknown[]) : []
  return s.enum.map((v, i) => ({ value: String(v), label: typeof names[i] === 'string' ? (names[i] as string) : String(v) }))
}

export function fieldOf(s: JSONSchema): Field {
  const t = typeOf(s)
  if (t === 'array') {
    const items = (s.items ?? {}) as JSONSchema
    const options = titled(items.anyOf) ?? titled(items.oneOf) ?? enumOptions(items)
    return options ? { kind: 'multi', options } : { kind: 'unknown' }
  }
  const options = titled(s.oneOf) ?? titled(s.anyOf) ?? enumOptions(s)
  if (options) return { kind: 'choice', options }
  if (t === 'boolean') return { kind: 'boolean' }
  if (t === 'number' || t === 'integer') return { kind: 'number', integer: t === 'integer' }
  if (t === 'string') {
    const f = s.format
    return { kind: 'text', format: f === 'email' || f === 'uri' || f === 'date' || f === 'date-time' ? f : undefined }
  }
  return { kind: 'unknown' }
}

/** The form's starting values: each field's default, else empty. */
export function initialValues(schema?: JSONSchema): Values {
  const v: Values = {}
  for (const [k, s] of Object.entries(schema?.properties ?? {})) {
    const f = fieldOf(s)
    if (s.default !== undefined && s.default !== null) {
      v[k] = f.kind === 'text' && f.format === 'date-time' ? toLocalInput(String(s.default)) : s.default
    } else if (f.kind === 'multi') v[k] = []
    else if (f.kind === 'boolean') v[k] = false
  }
  return v
}

const empty = (v: unknown) => v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)
const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/** Checks the values against the schema's limits; an empty result means the form can be sent. */
export function validate(schema: JSONSchema | undefined, values: Values): Errors {
  const errors: Errors = {}
  const required = new Set(schema?.required ?? [])
  for (const [k, s] of Object.entries(schema?.properties ?? {})) {
    const v = values[k]
    const f = fieldOf(s)
    if (empty(v)) {
      if (required.has(k) && f.kind !== 'boolean') errors[k] = f.kind === 'multi' ? 'Choose at least one.' : 'Required.'
      continue
    }
    if (f.kind === 'text') {
      const str = String(v)
      if (typeof s.minLength === 'number' && str.length < s.minLength) errors[k] = `At least ${s.minLength} characters.`
      else if (typeof s.maxLength === 'number' && str.length > s.maxLength) errors[k] = `At most ${s.maxLength} characters.`
      else if (f.format === 'email' && !EMAIL.test(str)) errors[k] = 'Enter an email address.'
      else if (f.format === 'uri' && !isURL(str)) errors[k] = 'Enter a full URL, such as https://example.com.'
      else if ((f.format === 'date' || f.format === 'date-time') && Number.isNaN(Date.parse(str))) errors[k] = 'Enter a valid date.'
    } else if (f.kind === 'number') {
      const n = Number(v)
      if (!Number.isFinite(n)) errors[k] = 'Enter a number.'
      else if (f.integer && !Number.isInteger(n)) errors[k] = 'Enter a whole number.'
      else if (typeof s.minimum === 'number' && n < s.minimum) errors[k] = `At least ${s.minimum}.`
      else if (typeof s.maximum === 'number' && n > s.maximum) errors[k] = `At most ${s.maximum}.`
    } else if (f.kind === 'multi') {
      const n = (v as unknown[]).length
      if (typeof s.minItems === 'number' && n < s.minItems) errors[k] = `Choose at least ${s.minItems}.`
      else if (typeof s.maxItems === 'number' && n > s.maxItems) errors[k] = `Choose at most ${s.maxItems}.`
    }
  }
  return errors
}

/** The content to send back: typed values, empty optional fields left out. */
export function toContent(schema: JSONSchema | undefined, values: Values): Values {
  const out: Values = {}
  for (const [k, s] of Object.entries(schema?.properties ?? {})) {
    const v = values[k]
    const f = fieldOf(s)
    if (f.kind === 'boolean') {
      out[k] = !!v
      continue
    }
    if (empty(v)) continue
    if (f.kind === 'number') out[k] = Number(v)
    else if (f.kind === 'text' && f.format === 'date-time') out[k] = new Date(String(v)).toISOString()
    else if (f.kind === 'choice') out[k] = matchType(s, String(v))
    else out[k] = v
  }
  return out
}

/** A choice keeps the type of the schema's value: an enum of numbers answers with a number. */
function matchType(s: JSONSchema, v: string): unknown {
  const all = [...(Array.isArray(s.enum) ? s.enum : []), ...((s.oneOf as { const?: unknown }[] | undefined) ?? []).map((o) => o.const)]
  return all.find((x) => String(x) === v) ?? v
}

function isURL(v: string) {
  try {
    const u = new URL(v)
    return !!u.protocol && !!u.host
  } catch {
    return false
  }
}

/** ISO date-time to the value a datetime-local input takes, in the browser's time zone. */
function toLocalInput(iso: string) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/**
 * Fields in display order: required fields first, in the order the schema's required list gives,
 * then the rest as received. Object key order does not survive the gateway (its rules engine
 * re-encodes replies), so the required list, an array, is the only order that reliably arrives.
 */
export function orderedFields(schema?: JSONSchema): [string, JSONSchema][] {
  const props = schema?.properties ?? {}
  const req = (schema?.required ?? []).filter((k) => k in props)
  return [...req.map((k) => [k, props[k]] as [string, JSONSchema]), ...Object.entries(props).filter(([k]) => !req.includes(k))]
}

export function ElicitationForm({
  schema,
  values,
  errors,
  onChange,
  disabled,
}: {
  schema?: JSONSchema
  values: Values
  errors: Errors
  onChange: (v: Values) => void
  disabled?: boolean
}) {
  const props = orderedFields(schema)
  const required = new Set(schema?.required ?? [])
  if (!props.length) return <p className="text-sm text-muted-foreground">Nothing to fill in. Accept to continue.</p>
  const set = (k: string, v: unknown) => onChange({ ...values, [k]: v })
  return (
    <div className="space-y-5">
      {props.map(([k, s]) => (
        <FieldRow key={k} name={k} schema={s} required={required.has(k)} value={values[k]} error={errors[k]} onChange={(v) => set(k, v)} disabled={disabled} />
      ))}
    </div>
  )
}

function FieldRow({
  name,
  schema: s,
  required,
  value,
  error,
  onChange,
  disabled,
}: {
  name: string
  schema: JSONSchema
  required: boolean
  value: unknown
  error?: string
  onChange: (v: unknown) => void
  disabled?: boolean
}) {
  const id = useId()
  const f = fieldOf(s)
  const label = s.title ?? name
  const hintId = `${id}-hint`
  const described = s.description || error ? hintId : undefined

  if (f.kind === 'boolean') {
    return (
      <div className="space-y-1.5">
        <div className="flex items-start gap-3 rounded-lg border border-border bg-elevated/40 p-3">
          <Checkbox id={id} checked={!!value} onCheckedChange={(c) => onChange(c === true)} disabled={disabled} aria-describedby={described} className="mt-0.5" />
          <div className="min-w-0">
            <label htmlFor={id} className="cursor-pointer text-sm font-medium text-foreground">
              {label}
              {required && <span className="ml-0.5 text-danger">*</span>}
            </label>
            {s.description && <p className="text-xs leading-5 text-muted-foreground">{s.description}</p>}
          </div>
        </div>
        {error && <FieldError id={hintId}>{error}</FieldError>}
      </div>
    )
  }

  let control: ReactNode
  switch (f.kind) {
    case 'text': {
      const type = f.format === 'email' ? 'email' : f.format === 'uri' ? 'url' : f.format === 'date' ? 'date' : f.format === 'date-time' ? 'datetime-local' : 'text'
      control = (
        <Input
          id={id}
          type={type}
          value={String(value ?? '')}
          onChange={(e) => onChange(e.target.value)}
          maxLength={typeof s.maxLength === 'number' ? s.maxLength : undefined}
          placeholder={f.format === 'uri' ? 'https://' : f.format === 'email' ? 'name@example.com' : undefined}
          disabled={disabled}
          aria-invalid={!!error}
          aria-describedby={described}
          className={cn(f.format === 'uri' && 'font-mono', error && 'border-danger/50')}
        />
      )
      break
    }
    case 'number':
      control = (
        <Input
          id={id}
          type="number"
          inputMode={f.integer ? 'numeric' : 'decimal'}
          step={f.integer ? 1 : 'any'}
          min={typeof s.minimum === 'number' ? s.minimum : undefined}
          max={typeof s.maximum === 'number' ? s.maximum : undefined}
          value={value === undefined || value === null ? '' : String(value)}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          aria-invalid={!!error}
          aria-describedby={described}
          className={cn('font-mono', error && 'border-danger/50')}
        />
      )
      break
    case 'choice':
      control = <Choices id={id} options={f.options} selected={value === undefined || value === null ? [] : [String(value)]} onToggle={(v) => onChange(String(value) === v && !required ? undefined : v)} disabled={disabled} described={described} />
      break
    case 'multi': {
      const sel = Array.isArray(value) ? (value as unknown[]).map(String) : []
      control = <Choices id={id} multi options={f.options} selected={sel} onToggle={(v) => onChange(sel.includes(v) ? sel.filter((x) => x !== v) : [...sel, v])} disabled={disabled} described={described} />
      break
    }
    default:
      control = <p className="text-xs text-muted-foreground">This field type is not part of an elicitation form, so it is left out.</p>
  }

  const limits = describeLimits(s, f)
  return (
    <div className="space-y-1.5">
      <label htmlFor={f.kind === 'choice' || f.kind === 'multi' ? undefined : id} id={`${id}-label`} className="block text-xs font-semibold text-muted-foreground">
        {label}
        {required && <span className="ml-0.5 text-danger">*</span>}
      </label>
      {control}
      {error ? (
        <FieldError id={hintId}>{error}</FieldError>
      ) : (
        (s.description || limits) && (
          <p id={hintId} className="text-xs leading-5 text-faint-foreground">
            {[s.description, limits].filter(Boolean).join(' ')}
          </p>
        )
      )}
    </div>
  )
}

function describeLimits(s: JSONSchema, f: Field): string | undefined {
  if (f.kind === 'number' && (typeof s.minimum === 'number' || typeof s.maximum === 'number')) {
    if (typeof s.minimum === 'number' && typeof s.maximum === 'number') return `${s.minimum} to ${s.maximum}.`
    return typeof s.minimum === 'number' ? `At least ${s.minimum}.` : `At most ${s.maximum}.`
  }
  if (f.kind === 'multi' && typeof s.maxItems === 'number') return `Up to ${s.maxItems}.`
  return undefined
}

function FieldError({ id, children }: { id: string; children: ReactNode }) {
  return (
    <p id={id} role="alert" className="text-xs leading-5 text-danger">
      {children}
    </p>
  )
}

/** Options as bordered tiles: radios for one choice, checkboxes for several. */
function Choices({
  id,
  options,
  selected,
  onToggle,
  multi,
  disabled,
  described,
}: {
  id: string
  options: Option[]
  selected: string[]
  onToggle: (v: string) => void
  multi?: boolean
  disabled?: boolean
  described?: string
}) {
  return (
    <div role={multi ? 'group' : 'radiogroup'} aria-labelledby={`${id}-label`} aria-describedby={described} className="flex flex-wrap gap-2">
      {options.map((o) => {
        const on = selected.includes(o.value)
        return (
          <button
            key={o.value}
            type="button"
            role={multi ? 'checkbox' : 'radio'}
            aria-checked={on}
            disabled={disabled}
            onClick={() => onToggle(o.value)}
            className={cn(
              'inline-flex h-9 items-center gap-2 rounded-lg border px-3 text-sm transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-50',
              on ? 'border-accent bg-accent/10 text-foreground' : 'border-border bg-elevated text-muted-foreground hover:border-border-strong hover:text-foreground',
            )}
          >
            <span
              aria-hidden
              className={cn(
                'grid size-4 shrink-0 place-items-center border transition-colors',
                multi ? 'rounded-[5px]' : 'rounded-full',
                on ? 'border-accent bg-accent text-on-accent' : 'border-border-strong',
              )}
            >
              {on && (multi ? <Check className="size-3" strokeWidth={3} /> : <span className="size-1.5 rounded-full bg-on-accent" />)}
            </span>
            {o.label}
          </button>
        )
      })}
    </div>
  )
}
