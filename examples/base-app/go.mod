module github.com/assurrussa/outbox/examples/base-app

go 1.26

require github.com/assurrussa/outbox v0.9.2

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
)

replace github.com/assurrussa/outbox => ../..
