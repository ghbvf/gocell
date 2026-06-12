-- Migration 060: add optimistic-concurrency `version` column to the `policies`
-- table (EPIC #1337 PR-9, issue #1347). Previously the policy write surface was
-- a blind upsert (Save); this migration unlocks Create / Update(CAS) /
-- Delete(CAS) by giving each row a monotonic version counter.
--
-- Column shape: INT NOT NULL DEFAULT 1 — new rows and back-filled existing rows
-- both start at version 1 (consistent with the "repo sets Version=1 on Create"
-- invariant in policy.go).
--
-- The UPDATE and DELETE CAS queries in policy_repo.go use
-- `WHERE ... AND version = $expected RETURNING version` to detect concurrent
-- updates; 0 rows returned → disambiguate not-found vs version-mismatch via a
-- follow-up existence check (see disambiguateCASMiss).
--
-- Non-destructive DDL (ALTER TABLE ... ADD COLUMN with DEFAULT — no existing row
-- is rewritten; the default supplies the value for pre-migration rows):
-- NO `+gocell forward-rebuild` annotation and NO Go destructive permit.
-- The Down section is a true reversible rollback (DROP COLUMN removes the column
-- and its default without touching other columns).
--
-- ref: adapters/postgres/migrations/059_create_policies.sql (table shape)
-- ref: corecells/accesscore/internal/ports/policy_repo.go (CAS interface)
-- ref: corecells/accesscore/internal/adapters/postgres/policy_repo.go (SQL)
-- ref: docs/plans/specs/1220-tenancy-abac-dataperm/ (tasks.md T9.x)

-- +goose Up

ALTER TABLE policies ADD COLUMN version INT NOT NULL DEFAULT 1;

-- +goose Down

ALTER TABLE policies DROP COLUMN version;
