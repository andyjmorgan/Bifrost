import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { KeySquare, LayoutGrid, Terminal, Waypoints } from 'lucide-react'
import { Button } from '../ui/components/button'
import { PageHeader } from '../components/PageHeader'
import { IconTile } from '../components/IconTile'
import { CopyButton } from '../components/CopyButton'
import { keyringEnabled } from '../keyring'

const INSTALL = 'curl -fsSL https://raw.githubusercontent.com/andyjmorgan/Bifrost/main/cli/install.sh | sh'
// The CLI's built-in address; any other gateway has to be named with BIFROST_ADDR.
const CLI_DEFAULT_ADDR = 'https://bifrost.donkeywork.dev/keyring'

/** Where a signed-in person lands: how to point an agent at the gateway, and how to install the CLI. */
export function GetStartedPage() {
  // The SPA is served by the gateway itself, so its origin is the gateway's public base.
  const base = window.location.origin
  const mcpUrl = `${base}/mcp`
  const keyringAddr = `${base}/keyring`
  const key = 'mcpg_your_key'

  const claudeCode = `claude mcp add --transport http bifrost ${mcpUrl} \\\n  --header "Authorization: Bearer ${key}"`
  const jsonConfig = JSON.stringify({ mcpServers: { bifrost: { type: 'http', url: mcpUrl, headers: { Authorization: `Bearer ${key}` } } } }, null, 2)

  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="Welcome"
        title="Get started"
        description="Bifrost is one MCP connection to every MCP server you have been granted. Point an agent at it with an API key, and use the bifrost CLI to fetch the credentials the keyring holds for you."
      />

      <Section
        icon={Waypoints}
        title="Connect an agent to the MCP server"
        hint="Any MCP client that speaks Streamable HTTP can connect. The gateway sends each request on to the right server, with that server's credential."
      >
        <Step n={1} title="Create an API key">
          <p>
            Create a key with the <Code>mcp:execute</Code> scope. A key acts as you and reaches the servers you hold. The secret is shown once, so copy it
            when it appears.
          </p>
          <Button variant="secondary" size="sm" asChild>
            <Link to="/api-keys">
              <KeySquare /> API keys
            </Link>
          </Button>
        </Step>

        <Step n={2} title="Pick a URL">
          <p>
            <Code>/mcp</Code> reaches every server you hold, with tool names prefixed by the server's slug, as in <Code>slug__tool</Code>. To reach one
            server with its own tool names, use its URL, <Code>/&lt;slug&gt;/mcp</Code>. Add <Code>?slugs=a,b</Code> to narrow <Code>/mcp</Code> to a
            few servers, and <Code>?search=true</Code> to offer the agent a small set of search tools in place of every tool.
          </p>
          <Snippet code={mcpUrl} />
          <Button variant="secondary" size="sm" asChild>
            <Link to="/portal">
              <LayoutGrid /> My servers and their URLs
            </Link>
          </Button>
        </Step>

        <Step n={3} title="Add it to your client">
          <p>For Claude Code, replace the key with your own:</p>
          <Snippet code={claudeCode} />
          <p>Most other clients take a JSON entry like this one:</p>
          <Snippet code={jsonConfig} />
          <p>
            A client that supports MCP authorization can sign in with your account and needs no key. It finds the sign-in details from the URL when
            the gateway answers its first request with a 401.
          </p>
        </Step>
      </Section>

      {keyringEnabled() && (
        <Section
          icon={Terminal}
          title="Use the bifrost CLI"
          hint="The bifrost CLI reads the secrets and OAuth tokens in your keyring, for shell scripts, CI jobs and agents that need a credential without keeping it in a file."
        >
          <Step n={1} title="Install it">
            <p>
              On Linux or macOS, amd64 or arm64, run the installer below. It downloads the newest release, checks it against the release's checksums,
              and installs it in <Code>~/.local/bin</Code>.
            </p>
            <Snippet code={INSTALL} />
            <p>
              Check the install with <Code>bifrost version</Code>.
            </p>
          </Step>

          <Step n={2} title="Sign in">
            {keyringAddr !== CLI_DEFAULT_ADDR && (
              <>
                <p>Point the CLI at this gateway first. Add the line to your shell profile to keep it:</p>
                <Snippet code={`export BIFROST_ADDR=${keyringAddr}`} />
              </>
            )}
            <p>
              Then sign in. The CLI shows a link to open in any browser, and it keeps the login and refreshes it for you. For automation, choose to
              paste a keyring access key instead, or set <Code>VAULT_API_KEY</Code>.
            </p>
            <Snippet code="bifrost auth login" />
          </Step>

          <Step n={3} title="Fetch credentials">
            <Snippet
              code={[
                'bifrost credentials list                 # what the keyring holds for you',
                'bifrost credentials get grafana-prod     # one secret, to stdout',
                'bifrost credentials header grafana-prod  # a ready "Name: value" header',
                'bifrost oauth get microsoft              # a live OAuth access token',
                'bifrost skill install                    # teach Claude to use the CLI',
              ].join('\n')}
            />
            <p>
              A header goes straight into curl, as in <Code>curl -H "$(bifrost credentials header grafana-prod)" …</Code>. Run{' '}
              <Code>bifrost --help</Code> for every command.
            </p>
          </Step>
        </Section>
      )}
    </div>
  )
}

function Section({ icon, title, hint, children }: { icon: typeof Terminal; title: string; hint: string; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <div className="flex items-start gap-3">
        <IconTile icon={icon} tone="accent" />
        <div>
          <h2 className="text-base font-semibold tracking-tight">{title}</h2>
          <p className="mt-0.5 max-w-2xl text-xs leading-5 text-muted-foreground">{hint}</p>
        </div>
      </div>
      <ol className="divide-y divide-border rounded-2xl border border-border bg-card">{children}</ol>
    </section>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="flex gap-4 p-5">
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-accent/10 font-mono text-xs font-semibold text-accent">{n}</span>
      <div className="min-w-0 flex-1 space-y-3 text-sm leading-6">
        <h3 className="font-medium">{title}</h3>
        {children}
      </div>
    </li>
  )
}

function Snippet({ code }: { code: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-border bg-code p-3">
      <pre className="min-w-0 flex-1 overflow-x-auto font-mono text-xs leading-5 text-code-foreground">{code}</pre>
      <CopyButton value={code} label="Copy" />
    </div>
  )
}

function Code({ children }: { children: ReactNode }) {
  return <code className="rounded bg-code px-1 py-0.5 font-mono text-xs text-code-foreground">{children}</code>
}
