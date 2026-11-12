import { useEffect, useRef } from 'react'
import { Annotation, EditorState, type ChangeSpec, type Extension } from '@codemirror/state'
import { EditorView, keymap, placeholder as placeholderExt } from '@codemirror/view'
import { autocompletion, closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { HighlightStyle, StreamLanguage, syntaxHighlighting, type StringStream } from '@codemirror/language'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { tags as t } from '@lezer/highlight'
import { celCompletionSource, type CelCompletionData } from './celCompletions'
import { cn } from '../../lib'

export type { CelCompletionData } from './celCompletions'

/* ---------- CEL tokenizer ---------- */

interface CelState {
  afterDot: boolean
}

const celLanguage = StreamLanguage.define<CelState>({
  name: 'cel',
  startState: () => ({ afterDot: false }),
  token(stream: StringStream, state: CelState) {
    if (stream.eatSpace()) return null
    const afterDot = state.afterDot
    state.afterDot = false

    // Strings: optional r/b prefix, single, double or triple quotes, backslash escapes unless raw.
    const prefix = stream.match(/^[rRbB]{1,2}(?=["'])/)
    const raw = !!prefix && /[rR]/.test(String((prefix as RegExpMatchArray)[0]))
    const q = stream.peek()
    if (q === '"' || q === "'") {
      const triple = stream.match(q.repeat(3))
      if (!triple) stream.next()
      const close = triple ? q.repeat(3) : q
      while (!stream.eol()) {
        if (!raw && stream.peek() === '\\') {
          stream.next()
          stream.next()
          continue
        }
        if (stream.match(close)) return 'string'
        stream.next()
      }
      return 'string'
    }
    if (prefix) return 'variableName'

    if (stream.match(/^0x[0-9a-fA-F]+u?/) || stream.match(/^(\d+\.\d*|\.\d+|\d+)([eE][+-]?\d+)?u?/)) return 'number'

    const word = stream.match(/^[A-Za-z_]\w*/) as RegExpMatchArray | null
    if (word) {
      const w = word[0]
      if (!afterDot) {
        if (w === 'true' || w === 'false') return 'bool'
        if (w === 'null') return 'null'
        if (w === 'in') return 'keyword'
      }
      if (stream.peek() === '(') return 'function'
      return afterDot ? 'property' : 'variable'
    }

    if (stream.match(/^(==|!=|<=|>=|&&|\|\||[-+*/%<>!?:])/)) return 'operator'
    const c = stream.next()
    if (c === '.') state.afterDot = true
    return 'punctuation'
  },
  tokenTable: {
    string: t.string,
    number: t.number,
    bool: t.bool,
    null: t.null,
    keyword: t.keyword,
    function: t.function(t.variableName),
    property: t.propertyName,
    variable: t.variableName,
    operator: t.operator,
    punctuation: t.punctuation,
  },
})

// Same hues as the JSON tree in PayloadViewer: strings warning, numbers info, booleans plum,
// null faint italic, punctuation muted, names in the code foreground.
const celHighlight = HighlightStyle.define([
  { tag: t.string, color: 'var(--warning)' },
  { tag: t.number, color: 'var(--info)' },
  { tag: t.bool, color: 'var(--plum)' },
  { tag: t.null, color: 'var(--faint-foreground)', fontStyle: 'italic' },
  { tag: t.keyword, color: 'var(--plum)' },
  { tag: [t.operator, t.punctuation], color: 'var(--muted-foreground)' },
  { tag: [t.variableName, t.propertyName, t.function(t.variableName)], color: 'var(--code-foreground)' },
])

/* ---------- theme ---------- */

const MONO = "'IBM Plex Mono', ui-monospace, SFMono-Regular, Menlo, monospace"
const SANS = "'IBM Plex Sans', ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif"

const celTheme = EditorView.theme({
  '&': { color: 'var(--code-foreground)', backgroundColor: 'transparent', fontSize: '12px' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: MONO, lineHeight: '20px', overflowX: 'auto' },
  '.cm-content': { padding: '0', caretColor: 'var(--foreground)' },
  '.cm-line': { padding: '0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--foreground)' },
  '.cm-placeholder': { color: 'var(--faint-foreground)' },
  '.cm-snippetField': { backgroundColor: 'color-mix(in srgb, var(--accent) 14%, transparent)', borderRadius: '3px' },
  '.cm-snippetFieldPosition': { borderLeft: '1px solid var(--accent)' },
  '.cm-tooltip': {
    border: '1px solid var(--border)',
    backgroundColor: 'var(--card)',
    color: 'var(--foreground)',
    borderRadius: '12px',
    boxShadow: '0 25px 50px -12px rgb(0 0 0 / 0.25)',
    overflow: 'hidden',
  },
  '.cm-tooltip.cm-tooltip-autocomplete': { marginTop: '4px' },
  '.cm-tooltip.cm-tooltip-autocomplete > ul': { fontFamily: MONO, fontSize: '12px', maxHeight: '18rem', minWidth: '16rem', maxWidth: 'min(32rem, calc(100vw - 2rem))', padding: '4px' },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > li': {
    display: 'flex',
    alignItems: 'baseline',
    gap: '16px',
    padding: '6px 10px',
    borderRadius: '8px',
    lineHeight: '20px',
    color: 'var(--foreground)',
  },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--elevated)', color: 'var(--foreground)' },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > completion-section': {
    padding: '6px 10px 2px',
    fontFamily: SANS,
    fontSize: '10px',
    fontWeight: '600',
    letterSpacing: '0.16em',
    textTransform: 'uppercase',
    color: 'var(--muted-foreground)',
    border: 'none',
  },
  '.cm-completionLabel': { flex: '0 1 auto' },
  '.cm-completionMatchedText': { textDecoration: 'none', color: 'var(--accent)' },
  '.cm-completionDetail': {
    marginLeft: 'auto',
    fontFamily: SANS,
    fontStyle: 'normal',
    fontSize: '11px',
    color: 'var(--muted-foreground)',
    whiteSpace: 'nowrap',
    maxWidth: '16rem',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  },
  '.cm-completionIcon': { display: 'none' },
})

/* ---------- component ---------- */

const External = Annotation.define<boolean>()

/** Folds newlines into spaces so a single-line editor stays on one line, including on paste. */
const singleLine: Extension = [
  EditorState.transactionFilter.of((tr) => {
    if (!tr.docChanged || tr.newDoc.lines === 1) return tr
    const changes: ChangeSpec[] = []
    tr.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
      changes.push({ from: fromA, to: toA, insert: inserted.toString().replace(/\n/g, ' ') })
    })
    return { changes, selection: tr.selection, effects: tr.effects, scrollIntoView: tr.scrollIntoView }
  }),
  keymap.of([{ key: 'Enter', run: () => true }]),
]

export interface CelEditorProps {
  value: string
  onChange: (value: string) => void
  onBlur?: () => void
  placeholder?: string
  singleLine?: boolean
  completions?: CelCompletionData
  invalid?: boolean
  ariaLabel?: string
  className?: string
}

/** A CodeMirror 6 input for a rule's CEL condition, dressed as the app's code textarea. */
export function CelEditor({ value, onChange, onBlur, placeholder, singleLine: oneLine, completions, invalid, ariaLabel, className }: CelEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const latest = useRef({ onChange, onBlur, completions })
  latest.current = { onChange, onBlur, completions }
  const initial = useRef(value)
  initial.current = value

  useEffect(() => {
    const ev = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: initial.current,
        extensions: [
          history(),
          closeBrackets(),
          oneLine ? singleLine : EditorView.lineWrapping,
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap]),
          celLanguage,
          syntaxHighlighting(celHighlight),
          celTheme,
          autocompletion({
            override: [
              celCompletionSource({ data: () => latest.current.completions }),
            ],
            icons: false,
            defaultKeymap: true,
            activateOnTyping: true,
            closeOnBlur: true,
          }),
          placeholder ? placeholderExt(placeholder) : [],
          EditorView.contentAttributes.of({
            spellcheck: 'false',
            autocorrect: 'off',
            autocapitalize: 'off',
            ...(ariaLabel ? { 'aria-label': ariaLabel } : {}),
          }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged && !u.transactions.some((tr) => tr.annotation(External))) latest.current.onChange(u.state.doc.toString())
          }),
          EditorView.domEventHandlers({ blur: () => void latest.current.onBlur?.() }),
        ],
      }),
    })
    view.current = ev
    return () => {
      ev.destroy()
      view.current = null
    }
  }, [oneLine, placeholder, ariaLabel])

  // Sync external value changes (the true/false toggle, a field switch) without echoing them.
  useEffect(() => {
    const ev = view.current
    if (!ev) return
    const current = ev.state.doc.toString()
    if (current !== value) ev.dispatch({ changes: { from: 0, to: current.length, insert: value }, annotations: External.of(true) })
  }, [value])

  return (
    <div
      ref={host}
      onMouseDown={(e) => {
        // Clicks in the padding should focus the editor like a textarea would.
        if (e.target === host.current) {
          e.preventDefault()
          view.current?.focus()
        }
      }}
      className={cn(
        'w-full min-w-0 cursor-text rounded-lg border border-border bg-code px-3 text-code-foreground transition-[border-color,box-shadow] duration-150 focus-within:border-accent focus-within:ring-[3px] focus-within:ring-accent/15',
        oneLine ? 'py-[9px]' : 'min-h-12 py-2',
        invalid && 'ring-[3px] ring-danger/20',
        className,
      )}
    />
  )
}
