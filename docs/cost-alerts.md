# Cost alerts for agents

`LangfuseCostAlert` watches the LLM cost of one agent in a Langfuse project and posts
to Slack when something changes in a way that needs attention, and again when it
recovers.

```yaml
apiVersion: langfuse.virtual-objects.github.io/v1alpha1
kind: LangfuseCostAlert
metadata:
  name: rca-agent-cost
spec:
  connectionRef: {name: my-project}
  agent:
    traceName: rca-agent        # the Langfuse trace name of the agent
    environment: production     # optional
    tags: [team:sre]            # optional, all must match
  rules:
    - name: hourly-budget
      budget: {window: 1h, maxCostUSD: "25"}
  notification:
    slack:
      webhookSecretRef: {name: slack-langfuse-alerts, key: webhook_url}
```

```console
$ kubectl get lfcostalert
NAME             AGENT       FIRING   READY   LAST EVAL   AGE
rca-agent-cost   rca-agent   1        True    2m          3d
```

## What is an "agent"?

Langfuse has no agent object. The operator identifies an agent by the traces it
writes: the **trace name** (for example `rca-agent`), optionally narrowed by
**environment** and **tags**, inside the project of the `LangfuseConnection`. With an
empty `agent` selector the whole project is watched.

Instrument each agent with a stable trace name; tags such as `team:sre` or
`agent:rca` work too.

## Scenarios

Each rule has a name and exactly one type. Combine several rules in one resource.

| Rule | Fires when | Typical cause |
|---|---|---|
| `budget` | Cost in the window is above `maxCostUSD` | Runaway loop, traffic burst, abuse |
| `change` with `increasePercent` | Cost rose by at least N% compared with an earlier window | Release that calls the LLM more often, retry storm |
| `change` with `decreasePercent` | Cost fell by at least N% | Agent stopped or broke, tracing broken, traffic moved |
| `costPerTrace` | The average cost of one agent run rose by N%, even at the same traffic | Switch to a pricier model, prompt or context growth, more tool calls |
| `unpricedUsage` | Generations used tokens but Langfuse computed no cost | New model ID without a model definition: cost reports show $0 |

### budget

```yaml
- name: hourly-budget
  budget:
    window: 1h
    maxCostUSD: "25"
```

### change: spikes and drops

```yaml
- name: daily-week-over-week
  change:
    window: 24h
    compareOffset: 168h     # same 24h one week earlier; default = previous window
    increasePercent: 50     # fire on +50% or more
    decreasePercent: 90     # fire on -90% or more
    minCostUSD: "10"        # ignore if both windows cost less than $10
```

- `compareOffset` must be at least the window. Week-over-week (`168h`) avoids alerts
  for normal weekday/weekend patterns.
- If the comparison window had no cost, the rule fires as "new spend" only when
  `minCostUSD` is set and the current window reaches it. Use a `budget` rule to cap
  brand-new spend.
- When neither window has traces, the state is `NoData` and nothing is sent.

### costPerTrace: each run got more expensive

```yaml
- name: cost-per-run
  costPerTrace:
    window: 6h
    compareOffset: 24h
    increasePercent: 100
    minTraces: 20           # both windows need at least 20 traces
```

This compares **cost ÷ number of traces**. Twice the traffic at the same cost per run
does not fire; the same traffic at twice the cost per run does.

### unpricedUsage: missing model definitions

```yaml
- name: unpriced-models
  unpricedUsage:
    window: 1h
    minTokens: 1000         # per model
```

The message lists the model IDs and their token counts. Fix it by adding a
`LangfuseModel` (see the [model definition runbook](runbook-model-definitions.md)).

## Notifications

```yaml
notification:
  slack:
    webhookSecretRef: {name: slack-langfuse-alerts, key: webhook_url}
    mentions: ["<!subteam^S0123ABCD>"]   # user group or <@U...>; firing messages only
  repeatInterval: 6h                      # re-send while still firing
  sendResolved: true                      # tell the channel when it recovers
```

1. In Slack, create an app with **Incoming Webhooks** for the target channel and copy
   the webhook URL.
2. Store it in a Secret, for example with External Secrets Operator
   ([example](../config/samples/slack-webhook-externalsecret.yaml)). The URL is a
   credential; the operator never logs it or writes it to status or events.
3. Optional: set `uiURL` on the `LangfuseConnection` (e.g. `https://langfuse.example.com`)
   so messages link to the project's traces in the browser.

A message contains the state, agent and rule, the current value and what it was
compared with, the project and environment, when it started firing, and a link to
Langfuse.

Delivery rules:

- A rule notifies when it starts firing, every `repeatInterval` while it keeps
  firing, and once when it recovers (`sendResolved`).
- Langfuse errors and missing data never resolve an active alert, so a Langfuse outage
  does not produce a false "resolved".
- A failed Slack post is retried after 30 seconds (Ready reason `NotificationFailed`).

## Timing

| Field | Default | Meaning |
|---|---|---|
| `interval` | `15m` | How often the rules are evaluated (minimum `1m`). Changing the spec evaluates immediately. |
| `ingestionDelay` | `5m` | Every window ends this long before "now", so traces that are still being ingested do not look like a drop. |
| `suspend` | `false` | Stop evaluating and notifying, e.g. during planned load tests. |

Windows can be up to 31 days. Each evaluation runs one or two Langfuse metrics queries
per rule, through the public `/api/public/metrics` endpoint with the project key.

## Status and metrics

`kubectl describe lfcostalert <name>` shows each rule's state (`OK`, `Firing`,
`NoData`, `Error`), value, baseline and message. The `Ready` condition reports
`Evaluated`, `ConnectionNotReady`, `SecretNotFound`, `NotificationFailed`, `APIError`,
`InvalidSpec` or `Suspended`. Firing and resolved transitions are also recorded as
Kubernetes events.

The operator exports Prometheus metrics on its metrics endpoint, so you can also build
dashboards or Alertmanager routes:

| Metric | Labels |
|---|---|
| `langfuse_cost_alert_rule_value` | `namespace`, `name`, `rule` |
| `langfuse_cost_alert_rule_firing` | `namespace`, `name`, `rule` |
| `langfuse_cost_alert_notifications_total` | `namespace`, `name`, `type`, `result` |

## Limits

- Costs come from Langfuse. Generations without a matching model definition count as
  $0 in every rule except `unpricedUsage`, so pair cost alerts with an
  `unpricedUsage` rule.
- Removing a rule from the spec drops its state without a "resolved" message.
- Slack is the only channel today. New channels implement the `notify.Sender`
  interface; see [extending.md](extending.md).
