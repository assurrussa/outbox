# Optional PostgreSQL observation

Pass `WithQueryTracer(tracer)` when constructing the existing client. It adds
no instrumentation by default, changes no Client/DBEngine interfaces, and never
mutates an existing pool. Supply a concurrency-safe pgx.QueryTracer; use
`pgx/multitracer.New` to compose observers. Other pgx tracing interfaces on the
same value are preserved (batch, copy, prepare, connect). Repeated options use
the last tracer, including nil.

This seam retains the existing SQL logger and OpenTracing behavior. In local/dev,
that logger interpolates arguments; do not scrape its output for a safe debug
panel. An observer receives SQL and arguments from pgx, but must discard or
sanitize them before retention/display. No logging or recording is performed
by this option itself.

Pool/transaction queries use pgx tracing, with the original request context.
QueryRow timing includes Scan; query timing completes when rows are closed or
consumed. A QueryTracer alone does not observe SendBatch/CopyFrom: implement the
corresponding pgx interfaces if those operations must be counted. Count each
layer once, not both high-level wrapper logging and underlying query tracing.

This does not instrument an independently created database/sql connection
(e.g. GoAuth); configure its driver separately. Background jobs need independent
correlation and must not retain the HTTP request collector indefinitely.

No public debug endpoint, production activation, migrations or new dependencies
are introduced by this seam.
