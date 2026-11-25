# Adding a new Langfuse resource kind

The operator is designed so that each Langfuse object (models today; score configs,
prompts or evaluators later) is a small, self-contained addition. This guide shows the
pieces to reuse and the rules every kind must follow.

## Building blocks

| Piece | Where | What it gives you |
|---|---|---|
| `LangfuseConnection` | `api/v1alpha1/langfuseconnection_types.go` | Host and project key, verified, with `status.projectID`. New kinds reference it through `connectionRef`. |
| `ConnectionResolver` | `internal/controller/common.go` | Turns a connection into an authenticated `*langfuse.Client`. Reads the Secret uncached and classifies failures (`SecretNotFound`, `InvalidSpec`). |
| `langfuse.Client` | `internal/langfuse/client.go` | HTTP client with auth, `X-From`, error classification (`IsNotFound`, `IsUnauthorized`, `IsRejected`), pagination. Add one method per endpoint. |
| Fake Langfuse | `internal/langfuse/fake` | In-memory API for unit and envtest tests. Extend it with the endpoints of your kind. |
| `readyConnection` | `internal/controller/common.go` | Loads a connection and returns a client, or the reason it is not usable yet. |
| `notify.Sender` | `internal/notify` | Notification channel interface (Slack today); add a channel by implementing `Send`. |
| `setReady`, `Finalizer`, `DeletionPolicy`, reasons | `internal/controller/common.go`, `api/v1alpha1/common_types.go` | A consistent `Ready` condition and deletion behaviour across kinds. |

## Steps

1. **Scaffold** the API and controller:
   ```sh
   kubebuilder create api --group langfuse --version v1alpha1 --kind LangfuseScoreConfig --resource --controller
   ```
2. **Types** (`api/v1alpha1/<kind>_types.go`):
   - include `ConnectionRef LocalObjectReference` and, if the object can be deleted in
     Langfuse, `DeletionPolicy`;
   - status has the remote ID, `ProjectID`, `ObservedGeneration` and `Conditions`;
   - mirror Langfuse's validation with kubebuilder markers and CEL rules, so invalid
     objects are rejected at `kubectl apply` instead of failing later;
   - register the types with `register(&Kind{}, &KindList{})`.
3. **Client**: add the endpoint methods to `internal/langfuse`, plus tests against the fake.
4. **Desired state and diff**: put the spec → request mapping and the comparison in their
   own package (see `internal/modeldef`), with table-driven unit tests. Keep it free of
   Kubernetes client code.
5. **Controller**, following `langfusemodel_controller.go`:
   - add the finalizer before creating anything remote;
   - resolve the connection; wait (no error) while it is not Ready;
   - find the existing remote object by stored ID, then by natural key (adoption);
   - create, update or recreate; record an event for every remote change;
   - map API errors: `IsUnauthorized` → `Unauthorized` with a long requeue,
     `IsRejected` → `Rejected` (the user must fix the spec), others → return the error
     for exponential backoff;
   - return `RequeueAfter: resync` when Ready, so drift is corrected;
   - patch status only when it changed.
6. **Watch the connection** with an index on `spec.connectionRef.name`, so objects
   react when their connection becomes Ready.
7. **Tests**: envtest specs in `internal/controller` (create, update, drift, adoption,
   delete, CEL rejections), and a real-Langfuse step in `test/e2e`.
8. **Docs and samples**: add a sample under `config/samples` (it is validated by a test)
   and a section in the README.

## Rules for all kinds

- Only use project-scoped public API endpoints. No database writes, no
  Enterprise-only admin API.
- Never log Secret values or full request bodies containing credentials.
- Keep references same-namespace.
- Every remote write must be idempotent and safe to repeat after a crash.
