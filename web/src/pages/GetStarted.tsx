import { useEffect, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Bot, KeySquare, LayoutGrid, Terminal, Waypoints, type LucideIcon } from 'lucide-react'
import { Button } from '../ui/components/button'
import { IconTile } from '../components/IconTile'
import { CopyButton } from '../components/CopyButton'
import { Logo } from '../components/Logo'
import { Tabs } from '../components/Tabs'
import { api, type PortalServerDTO } from '../api'
import { useMe } from '../me'
import { keyringEnabled } from '../keyring'
import { cn } from '../lib'

const INSTALL = 'curl -fsSL https://raw.githubusercontent.com/andyjmorgan/Bifrost/main/cli/install.sh | sh'
// The CLI's built-in address; any other gateway has to be named with BIFROST_ADDR.
const CLI_DEFAULT_ADDR = 'https://bifrost.donkeywork.dev/keyring'
const KEY = 'mcpg_your_key'
// How many of the caller's servers the hero's fan-out names before "+N more".
const FAN_OUT = 4

type ClientTab = 'claude' | 'json' | 'urls'
/** A command or URL, and one line on what it does. */
type Command = [code: string, what: string]

/** Where a signed-in person lands: how to point an agent at the gateway, and how to install the CLI. */
export function GetStartedPage() {
  const { me } = useMe()
  const [servers, setServers] = useState<PortalServerDTO[]>()
  useEffect(() => {
    api
      .portal()
      .then(setServers)
      .catch(() => setServers([]))
  }, [])

  // The SPA is served by the gateway itself, so its origin is the gateway's public base.
  const base = window.location.origin
  const mcpUrl = `${base}/mcp`
  const firstName = me?.name?.split(' ')[0]
  const keyring = keyringEnabled()

  return (
    <div className="space-y-6">
      <Hero firstName={firstName} mcpUrl={mcpUrl} servers={servers} />

      <div className={cn('grid gap-6', keyring && 'lg:grid-cols-2')}>
        <ConnectCard base={base} mcpUrl={mcpUrl} />
        {keyring && <CliCard base={base} />}
      </div>

    </div>
  )
}

function Hero({ firstName, mcpUrl, servers }: { firstName?: string; mcpUrl: string; servers?: PortalServerDTO[] }) {
  return (
    <section className="relative overflow-hidden rounded-3xl border border-border bg-card">
      {/* A soft accent glow and a faint dot grid, all through theme tokens so both modes hold. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            'radial-gradient(60% 90% at 0% 0%, color-mix(in srgb, var(--accent) 16%, transparent), transparent 70%), radial-gradient(50% 80% at 100% 100%, color-mix(in srgb, var(--plum) 12%, transparent), transparent 70%)',
        }}
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 opacity-40 [mask-image:linear-gradient(to_right,transparent,black_60%)]"
        style={{ backgroundImage: 'radial-gradient(var(--border-strong) 1px, transparent 1px)', backgroundSize: '18px 18px' }}
      />

      <div className="relative grid gap-8 p-6 sm:p-8 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] lg:items-center lg:p-10">
        <div className="space-y-6">
          <div className="space-y-3">
            <Logo compact imageClassName="size-11" />
            <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">{firstName ? `Welcome, ${firstName}` : 'Welcome to Bifrost'}</h1>
            <p className="max-w-xl text-sm leading-6 text-muted-foreground sm:text-base sm:leading-7">
              One MCP connection to every server you hold. Point an agent at the URL below with an API key, and the gateway sends each call to the right
              server with that server's credential.
            </p>
          </div>

          <div className="flex max-w-xl items-center gap-2 rounded-xl border border-border-strong bg-background/70 py-1.5 pl-4 pr-1.5 backdrop-blur">
            <span className="shrink-0 text-[10px] font-semibold uppercase tracking-[0.16em] text-accent">MCP</span>
            <code className="min-w-0 flex-1 truncate font-mono text-sm" title={mcpUrl}>
              {mcpUrl}
            </code>
            <CopyButton value={mcpUrl} label="Copy MCP URL" />
          </div>

          <div className="flex flex-wrap gap-2">
            <Button variant="primary" asChild>
              <Link to="/api-keys">
                <KeySquare /> Create an API key
              </Link>
            </Button>
            <Button variant="secondary" asChild>
              <Link to="/portal">
                <LayoutGrid /> My servers
              </Link>
            </Button>
          </div>
        </div>

        <FanOut servers={servers} />
      </div>
    </section>
  )
}

/** The hero's picture: your agent, one connection to the gateway, and the servers it reaches. */
function FanOut({ servers }: { servers?: PortalServerDTO[] }) {
  const shown = (servers ?? []).slice(0, FAN_OUT)
  const more = (servers?.length ?? 0) - shown.length
  return (
    <div className="flex items-center gap-3 sm:gap-4" aria-label="Your agent reaches your servers through Bifrost">
      <Node icon={Bot} label="Your agent" />
      <Wire />
      <Node icon={Waypoints} label="Bifrost" strong />
      <Wire />
      <div className="min-w-0 flex-1 space-y-1.5">
        {servers === undefined ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : shown.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border-strong p-3 text-xs leading-5 text-muted-foreground">
            No servers granted yet. Grant yourself one from{' '}
            <Link to="/servers" className="text-accent hover:underline">
              Servers
            </Link>
            .
          </div>
        ) : (
          <>
            {shown.map((s) => (
              <div
                key={s.slug}
                className={cn(
                  'flex items-center gap-2 rounded-lg border border-border bg-background/70 px-2.5 py-1.5 backdrop-blur',
                  !s.enabled && 'opacity-60',
                )}
                title={s.name}
              >
                <span className={cn('size-1.5 shrink-0 rounded-full', s.enabled ? 'bg-success' : 'bg-faint-foreground')} />
                <span className="truncate font-mono text-xs">{s.slug}</span>
              </div>
            ))}
            {more > 0 && (
              <Link to="/portal" className="block px-2.5 font-mono text-[11px] text-muted-foreground hover:text-foreground">
                +{more} more
              </Link>
            )}
          </>
        )}
      </div>
    </div>
  )
}

function Node({ icon: Icon, label, strong }: { icon: LucideIcon; label: string; strong?: boolean }) {
  return (
    <div className="flex shrink-0 flex-col items-center gap-1.5">
      <div
        className={cn(
          'grid size-12 place-items-center rounded-2xl border sm:size-14',
          strong ? 'border-accent/30 bg-accent/10 text-accent shadow-sm' : 'border-border bg-background/70 text-muted-foreground',
        )}
      >
        <Icon className="size-5 sm:size-6" />
      </div>
      <span className="text-[11px] font-medium text-muted-foreground">{label}</span>
    </div>
  )
}

function Wire() {
  return <div aria-hidden className="mb-5 h-px w-4 shrink-0 bg-gradient-to-r from-border-strong to-accent/60 sm:w-8" />
}

function ConnectCard({ base, mcpUrl }: { base: string; mcpUrl: string }) {
  const [tab, setTab] = useState<ClientTab>('claude')
  const claudeCode = `claude mcp add --transport http bifrost ${mcpUrl} \\\n  --header "Authorization: Bearer ${KEY}"`
  const jsonConfig = JSON.stringify({ mcpServers: { bifrost: { type: 'http', url: mcpUrl, headers: { Authorization: `Bearer ${KEY}` } } } }, null, 2)
  const urls: Command[] = [
    [`${base}/mcp`, 'Every server you hold, tools named slug__tool.'],
    [`${base}/<slug>/mcp`, 'One server, with its own tool names.'],
    [`${base}/mcp?slugs=a,b`, 'Just the servers you name.'],
    [`${base}/mcp?search=true`, 'A few search tools in place of every tool.'],
  ]

  return (
    <Card icon={Bot} tone="accent" title="Connect an agent" hint="Any MCP client that speaks Streamable HTTP.">
      <ol className="space-y-2.5 text-sm leading-6">
        <Numbered n={1}>
          Create an API key with the <Code>mcp:execute</Code> scope. The secret is shown once.
        </Numbered>
        <Numbered n={2}>Add the gateway to your client with the key in place of the placeholder.</Numbered>
      </ol>
      <div className="space-y-3">
        <Tabs<ClientTab>
          label="Client configuration"
          value={tab}
          onChange={setTab}
          tabs={[
            { key: 'claude', label: 'Claude Code' },
            { key: 'json', label: 'JSON config' },
            { key: 'urls', label: 'URLs' },
          ]}
        />
        <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`}>
          {tab === 'urls' ? <Commands items={urls} /> : <Snippet code={tab === 'claude' ? claudeCode : jsonConfig} />}
        </div>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">
        A client that supports MCP authorization can sign in with your account instead of a key. It finds the sign-in details from the URL.
      </p>
    </Card>
  )
}

function CliCard({ base }: { base: string }) {
  const addr = `${base}/keyring`
  const setup = [...(addr !== CLI_DEFAULT_ADDR ? [`export BIFROST_ADDR=${addr}`] : []), 'bifrost auth login'].join('\n')

  return (
    <Card icon={Terminal} tone="info" title="The bifrost CLI" hint="Secrets and OAuth tokens from your keyring, for scripts, CI and agents.">
      <Labeled n={1} label="Install on Linux or macOS">
        <Snippet code={INSTALL} />
      </Labeled>
      <Labeled n={2} label="Sign in from a browser link">
        <Snippet code={setup} />
      </Labeled>
    </Card>
  )
}

function Card({ icon, tone, title, hint, children }: { icon: LucideIcon; tone: 'accent' | 'info'; title: string; hint: string; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-5 rounded-2xl border border-border bg-card p-5 sm:p-6">
      <div className="flex items-start gap-3">
        <IconTile icon={icon} tone={tone} />
        <div className="min-w-0">
          <h2 className="text-base font-semibold tracking-tight">{title}</h2>
          <p className="mt-0.5 text-xs leading-5 text-muted-foreground">{hint}</p>
        </div>
      </div>
      {children}
    </section>
  )
}

function Numbered({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <Badge n={n} />
      <span className="min-w-0">{children}</span>
    </li>
  )
}

function Labeled({ n, label, children }: { n: number; label: string; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-3 text-sm font-medium">
        <Badge n={n} />
        {label}
      </div>
      {children}
    </div>
  )
}

function Badge({ n }: { n: number }) {
  return (
    <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-accent/10 font-mono text-[11px] font-semibold text-accent">
      {n}
    </span>
  )
}

function Snippet({ code }: { code: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-border bg-code p-3">
      <pre className="min-w-0 flex-1 whitespace-pre-wrap break-all font-mono text-xs leading-5 text-code-foreground">{code}</pre>
      <CopyButton value={code} label="Copy" />
    </div>
  )
}

/** Commands one to a row, each with what it does and its own copy button. */
function Commands({ items }: { items: Command[] }) {
  return (
    <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-code">
      {items.map(([code, what]) => (
        <li key={code} className="flex items-center gap-2 py-2 pl-3 pr-2">
          <div className="min-w-0 flex-1">
            <code className="block break-all font-mono text-xs leading-5 text-code-foreground">{code}</code>
            <p className="text-[11px] leading-4 text-muted-foreground">{what}</p>
          </div>
          <CopyButton value={code} label={`Copy ${code}`} />
        </li>
      ))}
    </ul>
  )
}

function Code({ children }: { children: ReactNode }) {
  return <code className="rounded bg-code px-1 py-0.5 font-mono text-xs text-code-foreground">{children}</code>
}
