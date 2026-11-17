import { EditorView } from '@codemirror/view'
import { HighlightStyle, type StringStream } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'

// CEL as the rule editor shows it: the tokenizer, the colours and the editor's theme. The rule
// text editor calls the tokenizer for everything on a line that is not one of its own words.

/* ---------- CEL tokenizer ---------- */

export interface CelState {
  afterDot: boolean
}

/** One CEL token. The rule text editor calls it for everything that is not one of its own words. */
export function celToken(stream: StringStream, state: CelState): string | null {
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
}

export const celTokenTable = {
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
}

// Same hues as the JSON tree in PayloadViewer: strings warning, numbers info, booleans plum,
// null faint italic, punctuation muted, names in the code foreground.
export const celHighlight = HighlightStyle.define([
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

export const celTheme = EditorView.theme({
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
  },
  '.cm-tooltip.cm-tooltip-autocomplete': { marginTop: '4px' },
  // The list clips to the rounded corners itself: the variable's description hangs beside it, outside the tooltip's box.
  '.cm-tooltip.cm-tooltip-autocomplete > ul': { borderRadius: '12px', fontFamily: MONO, fontSize: '12px', maxHeight: '18rem', minWidth: '16rem', maxWidth: 'min(32rem, calc(100vw - 2rem))', padding: '4px' },
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
  '.cm-tooltip.cm-completionInfo': {
    padding: '8px 10px',
    maxWidth: '20rem',
    fontFamily: SANS,
    fontSize: '12px',
    lineHeight: '18px',
    color: 'var(--muted-foreground)',
  },
})
