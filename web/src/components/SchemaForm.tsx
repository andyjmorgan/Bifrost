import { Checkbox } from '../ui/components/checkbox'
import { Input } from '../ui/components/input'
import { Textarea } from '../ui/components/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { FormField } from './FormField'
import type { JSONSchema } from '../api'

// A form generated from a tool's inputSchema. Scalars get native inputs; enums a select; arrays
// and objects a JSON textarea. Values are kept as strings in `draft` and coerced by `toArguments`.

export type Draft = Record<string, string>

function primaryType(s: JSONSchema): string {
  const t = Array.isArray(s.type) ? s.type.find((x) => x !== 'null') : s.type
  if (t) return t
  if (s.enum) return 'string'
  if (s.properties) return 'object'
  if (s.items) return 'array'
  return 'string'
}

export function initialDraft(schema?: JSONSchema): Draft {
  const d: Draft = {}
  for (const [k, s] of Object.entries(schema?.properties ?? {})) {
    if (s.default === undefined) continue
    const t = primaryType(s)
    d[k] = t === 'object' || t === 'array' ? JSON.stringify(s.default, null, 2) : String(s.default)
  }
  return d
}

/** Coerce the draft into tool arguments; throws with a field name when a value doesn't parse. */
export function toArguments(schema: JSONSchema | undefined, draft: Draft): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  const props = schema?.properties ?? {}
  const required = new Set(schema?.required ?? [])
  for (const [k, s] of Object.entries(props)) {
    const raw = draft[k]
    const t = primaryType(s)
    if (raw === undefined || raw === '') {
      if (t === 'boolean') {
        if (required.has(k)) out[k] = false
        continue
      }
      if (required.has(k)) throw new Error(`"${k}" is required.`)
      continue
    }
    switch (t) {
      case 'boolean':
        out[k] = raw === 'true'
        break
      case 'number':
      case 'integer': {
        const n = Number(raw)
        if (!Number.isFinite(n)) throw new Error(`"${k}" must be a number.`)
        out[k] = t === 'integer' ? Math.trunc(n) : n
        break
      }
      case 'object':
      case 'array':
        try {
          out[k] = JSON.parse(raw)
        } catch {
          throw new Error(`"${k}" must be valid JSON.`)
        }
        break
      default:
        out[k] = raw
    }
  }
  // Pass through anything the schema didn't declare (free-form JSON textarea).
  for (const [k, v] of Object.entries(draft)) {
    if (!(k in props) && v !== '') {
      try {
        out[k] = JSON.parse(v)
      } catch {
        out[k] = v
      }
    }
  }
  return out
}

export function SchemaForm({ schema, draft, onChange, disabled }: { schema?: JSONSchema; draft: Draft; onChange: (d: Draft) => void; disabled?: boolean }) {
  const props = Object.entries(schema?.properties ?? {})
  const required = new Set(schema?.required ?? [])
  if (!props.length) return <p className="text-xs text-muted-foreground">This tool takes no arguments.</p>
  const set = (k: string, v: string) => onChange({ ...draft, [k]: v })
  return (
    <div className="space-y-4">
      {props.map(([k, s]) => {
        const t = primaryType(s)
        const value = draft[k] ?? ''
        const hint = s.description
        const label = s.title ? `${k} · ${s.title}` : k
        if (s.enum) {
          return (
            <FormField key={k} label={label} required={required.has(k)} hint={hint}>
              <Select value={value} onValueChange={(v) => set(k, v)} disabled={disabled}>
                <SelectTrigger className="font-mono text-xs" aria-label={k}>
                  <SelectValue placeholder="Choose…" />
                </SelectTrigger>
                <SelectContent>
                  {s.enum.map((opt) => (
                    <SelectItem key={String(opt)} value={String(opt)} className="font-mono text-xs">
                      {String(opt)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FormField>
          )
        }
        if (t === 'boolean') {
          const id = `arg-${k}`
          return (
            <div key={k} className="flex items-start gap-3">
              <Checkbox id={id} className="mt-0.5" checked={value === 'true'} onCheckedChange={(v) => set(k, v === true ? 'true' : 'false')} disabled={disabled} />
              <label htmlFor={id} className="min-w-0 cursor-pointer">
                <span className="block font-mono text-xs font-semibold text-foreground">
                  {k}
                  {required.has(k) && <span className="ml-0.5 text-danger">*</span>}
                </span>
                {hint && <span className="mt-0.5 block text-xs leading-5 text-faint-foreground">{hint}</span>}
              </label>
            </div>
          )
        }
        if (t === 'object' || t === 'array') {
          return (
            <FormField key={k} label={label} required={required.has(k)} hint={hint ? `${hint} (JSON)` : `JSON ${t}`}>
              <Textarea
                value={value}
                onChange={(e) => set(k, e.target.value)}
                placeholder={t === 'array' ? '[]' : '{}'}
                className="min-h-24 font-mono text-xs"
                spellCheck={false}
                disabled={disabled}
              />
            </FormField>
          )
        }
        const numeric = t === 'number' || t === 'integer'
        const long = !numeric && (s.format === 'textarea' || (typeof s.maxLength === 'number' && s.maxLength > 200))
        return (
          <FormField key={k} label={label} required={required.has(k)} hint={hint}>
            {long ? (
              <Textarea value={value} onChange={(e) => set(k, e.target.value)} className="font-mono text-xs" disabled={disabled} />
            ) : (
              <Input
                type={numeric ? 'number' : 'text'}
                step={t === 'integer' ? 1 : 'any'}
                min={s.minimum}
                max={s.maximum}
                value={value}
                onChange={(e) => set(k, e.target.value)}
                placeholder={s.format ?? t}
                className="font-mono text-xs"
                disabled={disabled}
              />
            )}
          </FormField>
        )
      })}
    </div>
  )
}
