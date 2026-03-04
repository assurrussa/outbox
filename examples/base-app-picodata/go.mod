module github.com/assurrussa/outbox/examples/base-app-picodata

go 1.26

require (
	github.com/assurrussa/outbox v0.9.4
	github.com/assurrussa/outbox/backends/picodata v0.0.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.7.5 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/picodata/picodata-go v1.0.0 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/text v0.32.0 // indirect
)

replace github.com/assurrussa/outbox => ../..

replace github.com/assurrussa/outbox/backends/picodata => ../../backends/picodata
