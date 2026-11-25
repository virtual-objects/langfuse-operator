# Runbook: add or update a model definition

For anyone who maintains LLM cost tracking in Langfuse, e.g. a Langfuse focus group.
You change YAML in Git; the operator applies it to Langfuse. You do not need Langfuse
admin rights or database access.

**Time needed:** about 15 minutes, plus review.

## When to use this runbook

| Symptom or request | Go to |
|---|---|
| Generations show **$0 cost** or no model in Langfuse | [A. Add a model](#a-add-a-model) |
| A provider changed its prices, or we pay a different price (regional, negotiated) | [B. Change a price](#b-change-a-price) |
| A model is retired, or a definition must be renamed | [C. Remove or rename](#c-remove-or-rename-a-model) |
| A `LangfuseModel` is not `Ready` | [Troubleshooting](#troubleshooting) |

## Before you start

- Write access to the Git repository that holds your Langfuse YAML (the "config repo").
  It usually has one base folder shared by all environments and one overlay per
  environment; see [automation.md](automation.md#putting-it-together-one-bundle-many-environments).
- Viewer access to the Langfuse projects you change.
- Optional: read access (`kubectl get`) to the namespace where the `LangfuseModel`
  resources live, to check their status yourself.

Good to know:

- **A model definition belongs to one Langfuse project.** If several projects use the
  model, it needs a definition per project; in the config repo this normally means the
  same file listed in each project's overlay.
- **Costs are computed when a generation is ingested.** A new or changed price applies
  to new generations only. Past generations keep their cost.
- **Langfuse ships built-in models** (maintained by Langfuse). A project model with the
  same `modelName` overrides the built-in one for that project; the built-in one is
  never modified.

## A. Add a model

### 1. Find the exact model ID

Langfuse matches the `model` value that the application reports, which is often not
the marketing name. Examples: `anthropic.claude-sonnet-4-6`,
`us.anthropic.claude-sonnet-4-6`, `global.anthropic.claude-sonnet-4-6`, `gpt-4o-2024-08-06`.

In the Langfuse UI: open a trace, click the generation, and copy the **Model** field.
Collect every variant you see (with and without prefixes or version suffixes).

### 2. Check whether a definition is really missing

Langfuse UI → **Project Settings → Models → Test Model Match**, and paste each ID.

- **Matches a built-in model with the right price:** nothing to do. If you
  expected the built-in model but got none, your Langfuse version may predate it;
  upgrading Langfuse is the alternative to a custom definition.
- **Matches with the wrong price** (e.g. built-in list price but you pay the Bedrock
  regional rate): add a custom model (continue below). Use a `modelName` that
  differs from the built-in one, or the same name to override it.
- **No match:** continue below.

### 3. Get the price from an authoritative source

Use the provider's price list, not blog posts or screenshots, and note the source in
the pull request.

- **Amazon Bedrock:** the public AWS price list, via the helper in this repo:
  ```console
  $ hack/bedrock-prices.py "Claude Sonnet 4.6"
      3.0000  Million Input Tokens Global
      3.3000  Million Input Tokens Regional CRIS
     15.0000  Million Response Tokens Global
     16.5000  Million Response Tokens Regional CRIS
      0.3000  Million Cache Read Input Tokens Global
      0.3300  Million Cache Read Input Tokens Regional CRIS
      3.7500  Million Cache Write Input Tokens Global
      4.1250  Million Cache Write Input Tokens Regional CRIS
  ```
  `Global` is the `global.` inference profile. `Regional CRIS` covers the regional
  profiles: `us.`, `eu.`, `apac.`, `au.`, `jp.`.
- **Anthropic, OpenAI, Google:** the provider's public pricing page.
- **Contracted prices:** the contract owner (e.g. FinOps).

If prices differ per ID variant (global vs. regional), create **one definition per
price**, each with its own `matchPattern`.

### 4. Write the YAML

Copy this template into the base folder of the config repo, e.g.
`langfuse/base/models/<model-name>.yaml`:

```yaml
apiVersion: langfuse.virtual-objects.github.io/v1alpha1
kind: LangfuseModel
metadata:
  name: claude-sonnet-4-6-bedrock-geo          # Kubernetes name: lowercase, dashes
spec:
  connectionRef: {name: chat-agent}            # the project's LangfuseConnection
  modelName: claude-sonnet-4-6-bedrock-geo     # name shown in Langfuse
  matchPattern: '(?i)^(us|eu|apac|au|jp)\.anthropic\.claude-sonnet-4-6(-v\d+:\d+)?$'
  tokenizerId: claude                          # optional: claude | openai
  pricingTiers:
    - name: Standard
      isDefault: true
      pricePerMillion:                         # USD per 1M tokens, as quoted
        input: "3.30"
        output: "16.50"
        cacheRead: "0.33"
        cacheWrite: "4.125"
```

Rules of thumb:

- **Naming:** `modelName` = provider model + variant, e.g.
  `claude-sonnet-4-6-bedrock-geo`, `claude-sonnet-4-6-bedrock-global`. Use the same
  string for `metadata.name`.
- **`matchPattern`** is a PostgreSQL regular expression:
  - start with `(?i)` (case-insensitive) and anchor with `^ ... $`;
  - escape dots: `anthropic\.claude`;
  - make optional parts explicit: `(anthropic\.)?`, `(-v\d+:\d+)?`;
  - never use broad patterns like `.*sonnet.*`: they also match future models with
    different prices.
- **Prices** are strings with a dot as decimal separator. Copy them exactly as quoted
  per 1M tokens; the operator converts them without rounding.
- **Price names:**

  | Key | Langfuse usage types it sets |
  |---|---|
  | `input` | `input`, `input_tokens` |
  | `output` | `output`, `output_tokens` |
  | `cacheRead` | `input_cache_read`, `cache_read_input_tokens` |
  | `cacheWrite` | `input_cache_creation`, `cache_creation_input_tokens` |

  For anything else, use the literal usage type your SDK reports, e.g.
  `reasoning_tokens: "15"`. To find it, open a generation in Langfuse and look at its
  **usage details**.
- **Long-context pricing** (a higher price above N input tokens) needs a second tier;
  see `config/samples/langfuse_v1alpha1_langfusemodel_tiered.yaml`.

Add the file to the base `kustomization.yaml` (`resources:`) so every environment
gets it.

### 5. Check the pattern before opening the PR

Test every ID from step 1, plus IDs that must **not** match, with any PostgreSQL
(this is the regex engine Langfuse uses):

```sh
psql -Atc "SELECT id, id ~ '(?i)^(us|eu|apac|au|jp)\.anthropic\.claude-sonnet-4-6(-v\d+:\d+)?$'
           FROM (VALUES ('us.anthropic.claude-sonnet-4-6'),
                        ('global.anthropic.claude-sonnet-4-6'),
                        ('us.anthropic.claude-sonnet-4-60')) v(id)"
```

Expected: `t` for the IDs you want, `f` for the others. Alternatively, use **Test Model
Match** in Langfuse after the change is in staging.

If you have cluster access, let the API server validate the file (no changes are made):

```sh
kubectl apply --dry-run=server -n <namespace> -f langfuse/base/models/<model-name>.yaml
```

### 6. Open a pull request

Title, e.g. `langfuse: add claude-sonnet-4-6 Bedrock regional pricing`. In the
description include:

- the model IDs seen in traces (step 1) and the Test Model Match result (step 2);
- the price source (command output, or a link to the pricing page);
- the pattern test from step 5.

**Reviewer checklist**

- [ ] Prices match the cited source, per 1M tokens, for the right variant (global vs. regional).
- [ ] `matchPattern` is anchored, escapes dots, and matches nothing broader than intended.
- [ ] `modelName` follows the naming convention and is unique in the project.
- [ ] The file is listed in `kustomization.yaml`, so every environment gets it.

### 7. Roll out and verify

Staging first, then production, following your normal GitOps promotion.

1. **Ready in Kubernetes:**
   ```console
   $ kubectl get lfmodel -n <namespace>
   NAME                            MODEL                           CONNECTION   READY   AGE
   claude-sonnet-4-6-bedrock-geo   claude-sonnet-4-6-bedrock-geo   chat-agent   True    1m
   ```
2. **Present in Langfuse:** Project Settings → Models shows the model, and
   **Test Model Match** with a real ID returns it with the expected prices.
3. **Priced generations:** the next generation with that ID shows a non-zero cost.
   Check one by hand: tokens ÷ 1,000,000 × price per 1M.

## B. Change a price

1. Get the new price from the source (step 3 above).
2. Edit only the values under `pricePerMillion` in the existing file.
3. PR and review as in steps 6–7. Note in the PR when the new price took effect at
   the provider.

What happens: Langfuse v3 cannot update a model in place, so the operator deletes it
and creates it again with the new price (event `Recreated`; the model ID changes).
Only generations ingested **after** the change use the new price.

## C. Remove or rename a model

- **Remove:** delete the file and its `kustomization.yaml` entry. The operator deletes
  the model from Langfuse. To keep it in Langfuse while removing it from Git, first
  merge a change setting `deletionPolicy: Orphan`.
- **Rename:** change `spec.modelName` (and the file name, if you like). The operator
  removes the old definition and creates the new one.

## Troubleshooting

`kubectl describe lfmodel <name> -n <namespace>` shows the `Ready` condition and recent
events.

| Ready reason | Meaning | Fix |
|---|---|---|
| `Synced` | All good | — |
| `InvalidSpec` | The spec cannot be turned into a valid Langfuse model, e.g. tiers price different usage types | Read the message, fix the YAML |
| `Rejected` | Langfuse refused the model, e.g. `matchPattern is not a valid regex pattern (Postgres)` | Fix the YAML; test the pattern with step 5 |
| `Conflict` | Another `LangfuseModel` already manages this `modelName` for the same connection | Remove the duplicate file |
| `ConnectionNotReady` | The `LangfuseConnection` is missing or not ready | `kubectl describe lfconn <name>`; see below |
| `Unauthorized` | Langfuse rejected the API key | Ask the platform team to rotate the project key in the secret store |
| `APIError` | Langfuse unreachable or returned 5xx; retried automatically | Check Langfuse health; escalate if it persists |

Connection reasons: `SecretNotFound` means the Secret (or its `public_key` /
`secret_key` entries) does not exist yet. Check the ExternalSecret:
`kubectl get externalsecret -n <namespace>`.

`kubectl apply` errors such as `exactly one pricing tier must have isDefault: true`, or
an `Invalid value` error naming a field such as `pricePerMillion`, come from validation
and point at what to fix (for example a price written as `3,30` instead of `"3.30"`).

**Still $0 after the model is `Synced`?**

1. Test Model Match with the exact ID from the trace: maybe the pattern misses a
   prefix or suffix.
2. Compare the usage types in the generation's usage details with the priced ones.
   A usage type without a price adds $0.
3. Generations ingested before the change keep their old cost.

## Rollback

Revert the pull request. The operator restores the previous definition (or deletes the
new one). Generations ingested in between keep the cost they were given.
