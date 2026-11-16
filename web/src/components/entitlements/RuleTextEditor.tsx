import { useEffect, useRef } from 'react'
import { Annotation, Compartment, EditorState, type Extension } from '@codemirror/state'
import { Decoration, EditorView, keymap, lineNumbers, placeholder as placeholderExt } from '@codemirror/view'
import { autocompletion, closeBrackets, closeBracketsKeymap, type Completion, type CompletionContext, type CompletionSource } from '@codemirror/autocomplete'
import { HighlightStyle, StreamLanguage, syntaxHighlighting, type StringStream } from '@codemirror/language'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { tags as t } from '@lezer/highlight'
import { celHighlight, celTheme, celToken, celTokenTable, type CelState } from './CelEditor'
import { celCompletionSource, type CelCompletionData } from './celCompletions'
import type { HookSet } from '../../api'
import { cn } from '../../lib'

// The editor for one stage's rules, written as text:
//
//   when name == 'echo'          deny "Not today."
//   when !has(args.limit)        set args.limit = 50
//
// The words when, audit, deny, confirm and set are the format's own; everything else on a line is
// CEL, highlighted and completed as in the single-expression editor.

interface RuleState extends CelState {
  /** Nothing but spaces has been read on this line yet. */
  lineStart: boolean
}

const WORD = /^(audit|when|deny|confirm|set)(?![\w.(])/

const ruleLanguage = StreamLanguage.define<RuleState>({
  name: 'ruletext',
  startState: () => ({ afterDot: false, lineStart: true }),
  token(stream: StringStream, state: RuleState) {
    if (stream.sol()) state.lineStart = true
    if (stream.eatSpace()) return null
    if (state.lineStart && stream.peek() === '#') {
      stream.skipToEnd()
      return 'comment'
    }
    // The format's words: at the start of a line, or after a condition on a when line.
    if (!state.afterDot && stream.match(WORD)) {
      state.lineStart = false
      return 'ruleWord'
    }
    state.lineStart = false
    return celToken(stream, state)
  },
  tokenTable: { ...celTokenTable, comment: t.lineComment, ruleWord: t.definitionKeyword },
})

const ruleHighlight = HighlightStyle.define([
  { tag: t.lineComment, color: 'var(--faint-foreground)', fontStyle: 'italic' },
  { tag: t.definitionKeyword, color: 'var(--accent)', fontWeight: '600' },
])

const ruleTheme = EditorView.theme({
  '.cm-content': { padding: '10px 0' },
  '.cm-line': { padding: '0 12px 0 8px' },
  '.cm-gutters': { backgroundColor: 'transparent', border: 'none', color: 'var(--faint-foreground)' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 6px 0 12px', minWidth: '2.25rem', fontSize: '11px' },
  '.cm-problemLine': { backgroundColor: 'color-mix(in srgb, var(--danger) 12%, transparent)', boxShadow: 'inset 2px 0 0 var(--danger)' },
  '.cm-warningLine': { backgroundColor: 'color-mix(in srgb, var(--warning) 10%, transparent)', boxShadow: 'inset 2px 0 0 var(--warning)' },
})

const External = Annotation.define<boolean>()
const problemMark = Decoration.line({ class: 'cm-problemLine' })
const warningMark = Decoration.line({ class: 'cm-warningLine' })

/** Marks the lines with a problem, and those with only a warning. */
function markedLines(state: EditorState, problems: number[], warnings: number[]) {
  const inDoc = (n: number) => n >= 1 && n <= state.doc.lines
  const bad = new Set(problems.filter(inDoc))
  const all = [...new Set([...bad, ...warnings.filter(inDoc)])].sort((a, b) => a - b)
  return Decoration.set(all.map((n) => (bad.has(n) ? problemMark : warningMark).range(state.doc.line(n).from)))
}

const WORDS: Completion[] = [
  { label: 'when', type: 'keyword', detail: 'starts a rule', boost: 3 },
  { label: 'audit when', type: 'keyword', detail: 'a rule that only records', boost: 1 },
]

/** Completes the format's words and set fields at the start of a line, and CEL everywhere else. */
function ruleCompletions(data: () => RuleCompletionData | undefined): CompletionSource {
  const lineFrom = (ctx: CompletionContext) => ctx.state.doc.lineAt(ctx.pos).from
  const cel = celCompletionSource({ data, from: lineFrom })
  return (ctx) => {
    const line = ctx.state.doc.lineAt(ctx.pos)
    const before = line.text.slice(0, ctx.pos - line.from)
    if (/^\s*#/.test(before)) return null
    const d = data()
    const setField = /(?:^|\s)set\s+([\w.*/-]*)$/.exec(before)
    if (setField && d) {
      return {
        from: ctx.pos - setField[1].length,
        options: d.sets.map((s) => ({ label: s.field, type: 'property', detail: s.type })),
        validFor: /^[\w.*/-]*$/,
      }
    }
    const first = /^\s*(\w*)$/.exec(before)
    if (first && d) {
      const actions: Completion[] = d.actions.map((a) => ({ label: a, type: 'keyword', detail: a === 'deny' ? d.deny : a === 'confirm' ? 'ask the user first' : 'change a value' }))
      return { from: ctx.pos - first[1].length, options: [...WORDS, ...actions], validFor: /^\w*$/ }
    }
    return cel(ctx)
  }
}

export interface RuleCompletionData extends CelCompletionData {
  /** The actions the stage takes: deny, confirm, set. */
  actions: string[]
  /** What deny does at the stage. */
  deny?: string
  /** The fields a set action can name. */
  sets: HookSet[]
}

export interface RuleTextEditorProps {
  value: string
  onChange: (value: string) => void
  onBlur?: () => void
  /** 1-based lines that have a problem. */
  problemLines?: number[]
  /** 1-based lines that have a warning. */
  warningLines?: number[]
  completions?: RuleCompletionData
  placeholder?: string
  ariaLabel: string
  className?: string
}

/** A multi-line CodeMirror editor for one stage's rules, with line numbers and the lines that have problems marked. */
export function RuleTextEditor({ value, onChange, onBlur, problemLines: lines, warningLines, completions, placeholder, ariaLabel, className }: RuleTextEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const marks = useRef(new Compartment())
  const latest = useRef({ onChange, onBlur, completions })
  latest.current = { onChange, onBlur, completions }
  const initial = useRef(value)
  initial.current = value
  const lineKey = (lines ?? []).join(',') + '|' + (warningLines ?? []).join(',')

  useEffect(() => {
    const extensions: Extension[] = [
      lineNumbers(),
      history(),
      closeBrackets(),
      EditorView.lineWrapping,
      EditorState.tabSize.of(2),
      keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
      ruleLanguage,
      syntaxHighlighting(ruleHighlight),
      syntaxHighlighting(celHighlight),
      celTheme,
      ruleTheme,
      marks.current.of([]),
      autocompletion({ override: [ruleCompletions(() => latest.current.completions)], icons: false, defaultKeymap: true, activateOnTyping: true, closeOnBlur: true }),
      placeholder ? placeholderExt(placeholder) : [],
      EditorView.contentAttributes.of({ spellcheck: 'false', autocorrect: 'off', autocapitalize: 'off', 'aria-label': ariaLabel }),
      EditorView.updateListener.of((u) => {
        if (u.docChanged && !u.transactions.some((tr) => tr.annotation(External))) latest.current.onChange(u.state.doc.toString())
      }),
      EditorView.domEventHandlers({ blur: () => void latest.current.onBlur?.() }),
    ]
    const ev = new EditorView({ parent: host.current!, state: EditorState.create({ doc: initial.current, extensions }) })
    view.current = ev
    return () => {
      ev.destroy()
      view.current = null
    }
  }, [placeholder, ariaLabel])

  // Text set from outside (another stage, or an example added) replaces the document without echoing.
  useEffect(() => {
    const ev = view.current
    if (!ev) return
    const current = ev.state.doc.toString()
    if (current !== value) ev.dispatch({ changes: { from: 0, to: current.length, insert: value }, annotations: External.of(true) })
  }, [value])

  useEffect(() => {
    const ev = view.current
    if (!ev) return
    const [bad, warn] = lineKey.split('|').map((part) => (part ? part.split(',').map(Number) : []))
    ev.dispatch({ effects: marks.current.reconfigure(EditorView.decorations.of(markedLines(ev.state, bad, warn))) })
  }, [lineKey, value])

  return (
    <div
      ref={host}
      onMouseDown={(e) => {
        if (e.target === host.current) {
          e.preventDefault()
          view.current?.focus()
        }
      }}
      className={cn(
        'min-h-48 w-full min-w-0 cursor-text overflow-hidden rounded-xl border border-border bg-code text-code-foreground transition-[border-color,box-shadow] duration-150 focus-within:border-accent focus-within:ring-[3px] focus-within:ring-accent/15',
        className,
      )}
    />
  )
}
