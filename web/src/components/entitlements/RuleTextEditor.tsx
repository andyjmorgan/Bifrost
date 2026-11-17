import { useEffect, useRef } from 'react'
import { Annotation, Compartment, EditorState, type Extension } from '@codemirror/state'
import { Decoration, EditorView, keymap, lineNumbers, placeholder as placeholderExt } from '@codemirror/view'
import { autocompletion, closeBrackets, closeBracketsKeymap, snippetCompletion, type Completion, type CompletionSource } from '@codemirror/autocomplete'
import { HighlightStyle, StreamLanguage, syntaxHighlighting, type StringStream } from '@codemirror/language'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { tags as t } from '@lezer/highlight'
import { celHighlight, celTheme, celToken, celTokenTable, type CelState } from './celLanguage'
import { completeCel, completeSetPath, openString, setTypeOf, thenComplete, type CelCompletionData } from './celCompletions'
import type { HookSet } from '../../api'
import { cn } from '../../lib'

// The editor for one hook's rules, written as text:
//
//   when name == 'echo'          deny "Not today."
//   when !has(args.limit)        set args.limit = 50
//
// The words when, audit, deny, confirm and set are the format's own; everything else on a line is
// CEL.

interface RuleState extends CelState {
  /** Nothing but spaces has been read on this line yet. */
  lineStart: boolean
}

const WORD = /^(audit|when|deny|confirm|set)(?![\w.(])/

const ruleLanguage = StreamLanguage.define<RuleState>({
  name: 'ruletext',
  // Ctrl+/ comments lines out, which is how a rule is switched off.
  languageData: { commentTokens: { line: '#' } },
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

const RULE_START = /^\s*(?:audit\s+)?when(?:\s|$)/
const NAMED = /(?<![\w.])(?:upstream\.)?name\s*==\s*(['"])([^'"]+)\1/
const ENDS_OPEN = /(?:&&|\|\||[=!<>+\-*/%,([{?:]|\bin|\bwhen)$/

/** The item the rule around a line names in its condition: `flip` for when name == 'flip'. */
function namedItem(state: EditorState, lineNo: number): string | undefined {
  for (let n = lineNo; n >= 1; n--) {
    const text = state.doc.line(n).text
    if (RULE_START.test(text)) return NAMED.exec(text)?.[2]
  }
  return undefined
}

/** Whether brackets are balanced in text that holds no open string. */
function balanced(text: string): boolean {
  let depth = 0
  for (const c of text.replace(/'[^']*'|"[^"]*"/g, '')) {
    if ('([{'.includes(c)) depth++
    else if (')]}'.includes(c)) depth--
  }
  return depth === 0
}

const WHEN: Completion[] = [
  { label: 'when', type: 'keyword', detail: 'starts a rule', apply: 'when ', boost: 3 },
  { label: 'audit when', type: 'keyword', detail: 'a rule that only records', apply: 'audit when ', boost: 1 },
]

/** The hook's actions as completions: set opens its fields, confirm leaves the cursor in the question. */
function actionWords(d: RuleCompletionData, boost: number): Completion[] {
  return d.actions.map((a): Completion => {
    if (a === 'set') return { label: 'set', type: 'keyword', detail: 'change a value', apply: thenComplete('set '), boost }
    if (a === 'confirm') return snippetCompletion('confirm "${Question}?"', { label: 'confirm', type: 'keyword', detail: 'ask the user first', boost })
    return { label: a, type: 'keyword', detail: d.deny, boost }
  })
}

/**
 * Completes the format's words where they can stand, the path after set one part at a time, and
 * CEL everywhere else: in a condition, in a value, and inside the braces of a confirm question.
 */
function ruleCompletions(data: () => RuleCompletionData | undefined): CompletionSource {
  return (ctx) => {
    const line = ctx.state.doc.lineAt(ctx.pos)
    const before = line.text.slice(0, ctx.pos - line.from)
    if (/^\s*#/.test(before)) return null
    const base = data()
    if (!base) return null
    const d: RuleCompletionData = { ...base, itemName: namedItem(ctx.state, line.number) }
    const inString = openString(before)

    // The braces of a confirm question hold CEL; the rest of the question is plain text.
    const question = /(?:^|\s)confirm\s+(["'])((?:(?!\1).)*)$/.exec(before)
    if (question) {
      const open = question[2].lastIndexOf('{')
      if (open < 0 || question[2].indexOf('}', open) >= 0) return null
      return completeCel(ctx, d, ctx.pos - (question[2].length - open - 1))
    }

    if (!inString) {
      // set <path>: one part of the path at a time.
      const path = /(?:^|\s)set\s+([\w.*/-]*)$/.exec(before)
      if (path) return completeSetPath(ctx, path[1], d.sets, d)

      // set <path> = <value>: CEL, knowing the type the field takes.
      const value = /(?:^|\s)set\s+([\w.*/-]+)\s*=\s*(\S.*)?$/.exec(before)
      if (value) {
        const setType = setTypeOf(value[1], d.sets)
        if (value[2] === undefined) {
          // Nothing typed yet: offer only what the type calls for, never the whole list.
          if (setType === 'bool') return { from: ctx.pos, options: ['true', 'false'].map((label) => ({ label, type: 'keyword', detail: 'bool' })) }
          if (setType === 'duration') return { from: ctx.pos, options: [snippetCompletion("duration('${30s}')", { label: 'duration', type: 'function', detail: "('30s') duration" })] }
          return ctx.explicit ? completeCel(ctx, { ...d, setType }, ctx.pos) : null
        }
        return completeCel(ctx, { ...d, setType }, ctx.pos - value[2].length)
      }

      // The start of a line: the words that can begin one.
      const first = /^\s*(\w*)$/.exec(before)
      if (first) return { from: ctx.pos - first[1].length, options: [...WHEN, ...actionWords(d, 2)], validFor: /^\w*$/ }

      // After a finished condition on a when line: the action that follows it.
      const tail = /^(.*\S)(\s+)(\w*)$/.exec(before)
      if (tail && RULE_START.test(before) && !ENDS_OPEN.test(tail[1]) && balanced(tail[1])) {
        const actions = actionWords(d, 5)
        if (!tail[3]) return tail[2].length >= 2 || ctx.explicit ? { from: ctx.pos, options: actions, validFor: /^\w*$/ } : null
        const cel = completeCel(ctx, d, line.from)
        return { from: ctx.pos - tail[3].length, options: [...actions, ...(cel?.options ?? [])], validFor: /^\w*$/ }
      }
    }
    return completeCel(ctx, d, line.from)
  }
}

export interface RuleCompletionData extends CelCompletionData {
  /** The actions the hook takes: deny, confirm, set. */
  actions: string[]
  /** What deny does at the hook. */
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

/** A multi-line CodeMirror editor for one hook's rules, with line numbers and the lines that have problems marked. */
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

  // Text set from outside (another hook, or an example added) replaces the document without echoing.
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
