package skills

import (
	"fmt"
	"sync/atomic"
)

// URIs of the synthetic content set. Tests reference these rather than
// spelling URIs by hand.
const (
	NotebooksSkill    = "skill://notebooks/SKILL.md"
	NotebooksAdvanced = "skill://notebooks/references/advanced-analysis.md"

	WidgetsSkill      = "skill://widgets/SKILL.md"
	WidgetsTimeseries = "skill://widgets/references/timeseries.md"
	ChartsSkill       = "skill://widgets/charts/SKILL.md"
	ChartsPalette     = "skill://widgets/charts/assets/palette.json"

	BillingRefundsSkill = "skill://acme/billing/refunds/SKILL.md"
	BillingRefundsEmail = "skill://acme/billing/refunds/examples/email.md"
	SupportRefundsSkill = "skill://acme/support/refunds/SKILL.md"

	DailySkill  = "skill://reports/daily/SKILL.md"
	DailyLatest = "skill://reports/daily/data/latest.md"

	ReadmeResource = "file:///README.md"

	// SkillTemplate is the broad template that resolves any skill file.
	SkillTemplate = "skill://{+path}"
)

// DefaultListedResources are the URIs resources/list returns by default.
var DefaultListedResources = []string{NotebooksSkill, NotebooksAdvanced, ReadmeResource}

// DefaultFiles returns a fresh copy of the static synthetic files, keyed by
// URI. Every value is the exact byte content served.
func DefaultFiles() map[string][]byte {
	m := make(map[string][]byte, len(defaultFiles))
	for k, v := range defaultFiles {
		m[k] = []byte(v)
	}
	return m
}

var defaultFiles = map[string]string{
	NotebooksSkill: `---
name: notebooks
description: Analyze tabular data in notebooks, from loading to charting results.
license: Apache-2.0
metadata:
  author: bifrost-e2e
  version: "1.2"
  tags:
    - data
    - analysis
---

# Notebooks

Load the data, clean it, then analyze it.

For deeper statistical work read references/advanced-analysis.md before
starting.

To chart results, use the widgets skill at skill://widgets/SKILL.md. Time
series charts are covered in skill://widgets/references/timeseries.md.
`,
	NotebooksAdvanced: `# Advanced analysis

1. Check distributions before choosing a test.
2. Prefer robust estimators when outliers are present.
3. Record every transformation you apply.
`,

	WidgetsSkill: `---
name: widgets
description: Build dashboard widgets, including time series and charts.
---

# Widgets

Pick the widget type first. Time series widgets are described in
references/timeseries.md. Chart construction lives in the nested charts skill
under charts/.
`,
	WidgetsTimeseries: `# Time series widgets

- Always label the time axis with its timezone.
- Downsample to at most 500 points per series.
`,
	ChartsSkill: `---
name: charts
description: Render bar, line and pie charts with a consistent palette.
---

# Charts

Use the palette in assets/palette.json for every series.
`,
	ChartsPalette: `{"series":["#2f6f5e","#c05a2b","#4a5dbd","#a33d74"]}
`,

	BillingRefundsSkill: `---
name: refunds
description: Process customer refund requests per billing policy.
license: Apache-2.0
---

# Billing refunds

Refunds over 500 EUR need a second approver. Draft the customer reply from
examples/email.md.
`,
	BillingRefundsEmail: `Hello {name},

Your refund of {amount} has been approved.
`,
	SupportRefundsSkill: `---
name: refunds
description: Triage refund questions from support tickets before escalation.
---

# Support refunds

Confirm the order number, then hand the ticket to billing. Support never
issues refunds directly.
`,

	ReadmeResource: `# Skills test server

This resource is not part of any skill.
`,
}

// dailyFrontmatter is the fixed frontmatter of the dynamic skill.
const dailyFrontmatter = `---
name: daily
description: Assemble today's operational report from live data.
---
`

// dynamicFiles returns generators for the dynamic skill. Each read yields
// different bytes, which is why the skill advertises "resources": "dynamic".
func dynamicFiles() (roots []string, gens map[string]func() []byte) {
	var n atomic.Int64
	gens = map[string]func() []byte{
		DailySkill: func() []byte {
			return fmt.Appendf(nil, "%s\n# Daily report\n\nGeneration %d. Read data/latest.md for the numbers.\n", dailyFrontmatter, n.Add(1))
		},
		DailyLatest: func() []byte {
			return fmt.Appendf(nil, "# Latest\n\nrequests_served: %d\n", n.Add(1))
		},
	}
	return []string{"skill://reports/daily"}, gens
}
