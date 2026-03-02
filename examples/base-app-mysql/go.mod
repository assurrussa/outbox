module github.com/assurrussa/outbox/examples/base-app-mysql

go 1.26

require (
	github.com/assurrussa/outbox v0.0.0
	github.com/assurrussa/outbox/backends/mysql v0.0.0
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/go-sql-driver/mysql v1.9.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/pressly/goose/v3 v3.26.0 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
)

replace github.com/assurrussa/outbox => ../..

replace github.com/assurrussa/outbox/backends/mysql => ../../backends/mysql
