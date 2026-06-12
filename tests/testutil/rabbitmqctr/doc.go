// Package rabbitmqctr owns the RabbitMQ testcontainer helpers as a standalone module
// so the testcontainers-go/modules/rabbitmq dependency stays out of every other
// module's graph (only adapters/rabbitmq and tests/integration — the rightful
// rabbitmq users — and this module declare it). The helpers themselves live in
// rabbitmqctr.go behind the `integration` build tag.
package rabbitmqctr
