package archtest

// INVARIANT: SESSIONREFRESH-NO-SESSION-CREATE-01
//
// Refresh path must not mutate session.Store. session.ID is stable from
// login to logout (OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid
// stability + ory/fosite Session.Clone + zitadel oidc_session aggregate +
// keycloak findOfflineUserSession). Refresh rotates the refresh-token chain
// and mints a new access JWT carrying the same sid claim — the session row
// is never created, revoked, or rotated from this path.
//
// 历史：commit fd954cb8 引入 "revoke-old + create-new session per refresh"
// 设计，调用 session.Store.Revoke(oldID) + session.Store.Create(newID) 来轮换
// session UUID。该设计偏离 OAuth2/OIDC 业界惯例且与 refresh chain 一致性
// 域冲突（child refresh row 仍继承旧 session_id，二次 refresh 失败）。在 PR
// #482 review 中撤回。本 archtest 保证未来 AI session 不会重新引入该模式。
//
// AI-robust 评级：Medium (archtest type-aware) — type system 不强制
// session.Store.Create 在哪个 slice 不可调用，但 typeseval.ResolveMethodCall
// 让违反在 CI 时确定可见。Hard 形态需要把 session.Store 拆成 read-only +
// mutable 两个 sealed marker 由 composition root wrap，本 PR 范围外。
//
// 单条独立规则，按 ai-robust.md "{rule}_test.go" 命名。
//
// Detector logic (CheckSessionrefreshNoSessionCreate01, scanSessionrefreshFile,
// receiverNamedType, bannedSessionStoreMethods) lives in
// sessionrefresh_no_session_create.go (non-test) so it can be compiled by
// external Cell repositories. This file is the thin dogfood wrapper.

import (
	"testing"
)

// TestSessionrefreshNoSessionStoreMutation_01 fires when any file in
// cells/accesscore/slices/sessionrefresh (excluding _test.go) calls a banned
// method on session.Store. The rule resolves call targets through
// typeseval.ResolveMethodCall, so method-call (`s.Create(...)`), method-
// expression (`session.Store.Create(s, ...)`), and embedded-field promotion
// all collapse to the same *types.Func identity.
func TestSessionrefreshNoSessionStoreMutation_01(t *testing.T) {
	t.Parallel()
	Report(t, ruleSessionrefreshNoSessionCreate01, CheckSessionrefreshNoSessionCreate01(t, ConfigForExternalCell{}))
}
