module github.com/assurrussa/outbox/examples/base-app-pgsql

go 1.26

require (
	github.com/assurrussa/outbox v0.0.0
	github.com/assurrussa/outbox/backends/pgsql v0.0.0
	github.com/jackc/pgx/v5 v5.7.5
)

require (
	github.com/Masterminds/squirrel v1.5.4 // indirect
	github.com/georgysavva/scany/v2 v2.1.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/lann/builder v0.0.0-20180802200727-47ae307949d0 // indirect
	github.com/lann/ps v0.0.0-20150810152359-62de8c46ede0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/opentracing/opentracing-go v1.2.0 // indirect
	github.com/pressly/goose/v3 v3.26.0 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/vgarvardt/pgx-google-uuid/v5 v5.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	modernc.org/sqlite v1.46.1 // indirect
)

replace github.com/assurrussa/outbox => ../..

replace github.com/assurrussa/outbox/backends/pgsql => ../../backends/pgsql
