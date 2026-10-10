package http_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// route is one (method, path) pair in spec-normalized form: paths use
// {param} placeholders (e.g. /admin/accounts/{id}), never gin :param form.
type route struct {
	method, path string
}

// routeManifest is the SINGLE hardcoded path list in this package. It
// replaces requiredClientPaths (openapi_test.go, deleted by REQ-1) and is the
// source of truth for the served control-plane surface: every documented
// operation AND every served, non-excluded route must appear here. The four
// invariants in this file (M1..M4) keep manifest <-> router <-> spec aligned
// in both directions; deleting an entry makes at least one assertion fail.
//
// Coverage arithmetic: 42 documented operations (including capabilities/intents)
// + 22 previously-undocumented served control-plane routes (direction b) = 64 entries.
var routeManifest = []route{
	// -- documented client contract (spec operations) --
	{method: "GET", path: "/account-datasets"},
	{method: "GET", path: "/me"},
	{method: "GET", path: "/me/capabilities"},
	{method: "POST", path: "/command-intents"},
	{method: "GET", path: "/command-intents/{id}"},
	{method: "POST", path: "/command-intents/{id}/execute"},
	{method: "GET", path: "/me/profile"},
	{method: "PATCH", path: "/me/profile"},
	{method: "GET", path: "/me/sources"},
	{method: "GET", path: "/me/memberships"},
	{method: "GET", path: "/me/data"},
	{method: "GET", path: "/me/overview"},
	{method: "GET", path: "/me/activity"},
	{method: "POST", path: "/me/sync"},
	{method: "POST", path: "/me/export"},
	{method: "POST", path: "/me/erase"},
	{method: "GET", path: "/sync/jobs"},
	{method: "GET", path: "/sync/jobs/{id}"},
	{method: "POST", path: "/sync/jobs/{id}/reconcile"},
	{method: "GET", path: "/sync/jobs/{id}/export/download"},
	{method: "GET", path: "/operations"},
	{method: "POST", path: "/operations"},
	{method: "GET", path: "/operations/{id}"},
	{method: "POST", path: "/operations/{id}/run"},
	{method: "POST", path: "/operations/{id}/reconcile"},
	{method: "GET", path: "/audit/events"},
	{method: "GET", path: "/audit/operations/{id}"},
	{method: "POST", path: "/audit/replays"},
	{method: "POST", path: "/audit/replays/projection"},
	{method: "GET", path: "/audit/replays/{id}"},
	{method: "POST", path: "/audit/export"},
	{method: "GET", path: "/admin/callback-subscriptions"},
	{method: "POST", path: "/admin/callback-subscriptions"},
	{method: "DELETE", path: "/admin/callback-subscriptions/{id}"},
	{method: "GET", path: "/admin/audit/holds"},
	{method: "POST", path: "/admin/audit/holds"},
	{method: "DELETE", path: "/admin/audit/holds/{scope_type}/{scope_id}"},
	{method: "POST", path: "/admin/audit/anchor"},
	{method: "GET", path: "/admin/audit/anchor/manifest/{source}/{partition}"},
	{method: "POST", path: "/admin/regions/promote"},
	{method: "GET", path: "/admin/regions/directory/{account_id}"},
	// -- callback replay, canonical form (REQ-7, Option A): the served route
	// (callback.go:29) is the source of truth and the spec was aligned to it. --
	{method: "POST", path: "/admin/callback-subscriptions/deliveries/{id}/replay"},
	// -- control-plane routes served but undocumented until the spec additions
	// (direction b; each is registered in the handlers listed) --
	{method: "GET", path: "/me/resources"},                                         // aggregation.go:26
	{method: "GET", path: "/admin/accounts"},                                       // account.go:47
	{method: "GET", path: "/admin/accounts/{id}"},                                  // account.go:48
	{method: "POST", path: "/admin/accounts/backfill/legacy-identities"},           // account.go:56
	{method: "POST", path: "/admin/migration/legacy/compare"},                      // account.go:74 (cond. migration)
	{method: "GET", path: "/admin/migration/legacy/cutover/{tenant_id}"},           // account.go:83 (cond. cutover)
	{method: "POST", path: "/admin/migration/legacy/cutover"},                      // account.go:84 (cond. cutover)
	{method: "GET", path: "/admin/migration/legacy/cleanup-readiness/{tenant_id}"}, // account.go:85 (cond. cutover)
	{method: "GET", path: "/admin/accounts/{id}/overview"},                         // aggregation.go:34
	{method: "GET", path: "/admin/accounts/{id}/data"},                             // aggregation.go:35
	{method: "POST", path: "/admin/accounts/{id}/sync"},                            // sync_job.go:31
	{method: "POST", path: "/admin/accounts/{id}/export"},                          // sync_job.go:33
	{method: "POST", path: "/admin/accounts/{id}/erase"},                           // sync_job.go:35
	{method: "GET", path: "/admin/audit-log"},                                      // admin.go:56
	{method: "GET", path: "/admin/roles"},                                          // admin.go:57
	{method: "GET", path: "/admin/roles/{uid}"},                                    // admin.go:58
	{method: "POST", path: "/admin/roles/grant"},                                   // admin.go:65
	{method: "POST", path: "/admin/roles/revoke"},                                  // admin.go:66
	{method: "POST", path: "/admin/bans"},                                          // admin.go:74 (cond. bans)
	{method: "GET", path: "/admin/bans"},                                           // admin.go:75 (cond. bans)
	{method: "DELETE", path: "/admin/bans/{uid}"},                                  // admin.go:76 (cond. bans)
	{method: "GET", path: "/audit/verify"},                                         // audit_ledger.go:44
}

// ---------------------------------------------------------------------------
// Exclusion set (REQ-6)
// ---------------------------------------------------------------------------

// excludedPrefix is one declared-excluded route prefix. Every entry outside
// defaultExclusionPrefixes MUST carry a non-empty written justification;
// TestExclusionSetIsPinned asserts the set is exact and the justifications
// are present, so widening the exclusions to silence a control-plane route
// requires a deliberate, reviewable edit.
type excludedPrefix struct {
	prefix        string
	justification string
}

// defaultExclusionPrefixes — the legacy-compat surfaces excluded by the
// direction's acceptance criteria without an individual reason.
var defaultExclusionPrefixes = []string{
	"/blocks", "/comments", "/files", "/messages", "/search", "/events", "/internal/v1/events",
}

// excludedRoutePrefixes — served routes outside the client/control-plane
// contract.
var excludedRoutePrefixes = []excludedPrefix{
	{prefix: "/blocks", justification: "legacy compat: block surface"},
	{prefix: "/comments", justification: "legacy compat: comment surface"},
	{prefix: "/files", justification: "legacy compat: file surface (incl. /files/dl)"},
	{prefix: "/messages", justification: "legacy compat: message surface"},
	{prefix: "/search", justification: "legacy compat: search surface"},
	{prefix: "/events", justification: "legacy compat: SSE hub surface"},
	{prefix: "/internal/v1/events", justification: "legacy compat: source event ingest surface"},
	{prefix: "/conversations", justification: "legacy IM conversation surface (message.go:35-36)"},
	{prefix: "/notifications", justification: "legacy IM notification surface (notification.go:22-27)"},
	{prefix: "/notification-preferences", justification: "legacy IM preference surface (preference.go:22-23)"},
	{prefix: "/healthz", justification: "operational probe (router.go:72-76)"},
	{prefix: "/readyz", justification: "operational probe (router.go:72-76)"},
	{prefix: "/metrics", justification: "operational probe (router.go:72-76)"},
	{prefix: "/health/sources", justification: "operational probe (source_health.go:26)"},
}

// declaredExclusionSet is the exact, ordered pin of the exclusion prefixes;
// TestExclusionSetIsPinned asserts excludedRoutePrefixes equals it. Changing
// the exclusion policy requires editing BOTH lists deliberately.
var declaredExclusionSet = []string{
	"/blocks", "/comments", "/files", "/messages", "/search", "/events", "/internal/v1/events",
	"/conversations", "/notifications", "/notification-preferences",
	"/healthz", "/readyz", "/metrics", "/health/sources",
}

// ---------------------------------------------------------------------------
// Pure checker helpers (REQ-5): each has positive and negative fixtures in
// TestReconcile_CheckerFixtures, mirroring the scripts/check-architecture.sh
// fixture-pair doctrine.
// ---------------------------------------------------------------------------

// normalizeRoute converts a gin route path to spec-normalized form:
// "/admin/accounts/:id" -> "/admin/accounts/{id}". Literal segments are
// untouched.
var routeParamPattern = regexp.MustCompile(`:[^/]+`)

func normalizeRoute(path string) string {
	return routeParamPattern.ReplaceAllStringFunc(path, func(param string) string {
		return "{" + strings.TrimPrefix(param, ":") + "}"
	})
}

// excludeRoute reports whether a spec-normalized path is a declared-excluded
// legacy/operational surface (prefix match on segment boundaries).
func excludeRoute(path string) bool {
	for _, p := range excludedRoutePrefixes {
		if path == p.prefix || strings.HasPrefix(path, p.prefix+"/") {
			return true
		}
	}
	return false
}

// routesFromSpec flattens the parsed OpenAPI spec into (method, path) pairs.
// Paths are already in spec-normalized {param} form.
func routesFromSpec(spec *openAPISpec) []route {
	var out []route
	for path, operations := range spec.Paths {
		for method := range operations {
			if method == "parameters" {
				continue
			}
			out = append(out, route{method: strings.ToUpper(method), path: path})
		}
	}
	return out
}

// servedRoutes enumerates the router's served surface in spec-normalized
// form. Auth-group routes are registered under /v1 (router.go:80) and the
// prefix is stripped before comparison; public/internal routes carry no /v1.
func servedRoutes(e *gin.Engine) []route {
	var out []route
	for _, info := range e.Routes() {
		path := strings.TrimPrefix(info.Path, "/v1")
		out = append(out, route{method: info.Method, path: normalizeRoute(path)})
	}
	return out
}

func containsRoute(routes []route, want route) bool {
	for _, r := range routes {
		if r == want {
			return true
		}
	}
	return false
}

// diffRoutes returns the entries of want absent from have.
func diffRoutes(want, have []route) []route {
	var missing []route
	for _, r := range want {
		if !containsRoute(have, r) {
			missing = append(missing, r)
		}
	}
	return missing
}

// documentedButNotServed is direction (a): spec entries with no served route.
func documentedButNotServed(specRoutes, served []route) []route {
	return diffRoutes(specRoutes, served)
}

// servedButNotDocumented is direction (b): served routes with no spec entry.
func servedButNotDocumented(served, specRoutes []route) []route {
	return diffRoutes(served, specRoutes)
}

// ---------------------------------------------------------------------------
// Triple invariant M1..M4 (REQ-4): each is an independent t.Errorf-producing
// assertion; removing one manifest entry makes at least one of them fail.
// ---------------------------------------------------------------------------

// M1: manifest subset-of served.
func TestReconcile_ManifestIsServed(t *testing.T) {
	served := servedRoutes(newFullyWiredRouter(t))
	for _, missing := range diffRoutes(routeManifest, served) {
		t.Errorf("M1: manifest route %s %s is not served by the fully-wired router", missing.method, missing.path)
	}
}

// M2: served in-scope subset-of manifest (deleting manifest entries to
// silence direction (b) trips this).
func TestReconcile_ManifestCoversServed(t *testing.T) {
	served := servedRoutes(newFullyWiredRouter(t))
	for _, r := range served {
		if excludeRoute(r.path) {
			continue
		}
		if !containsRoute(routeManifest, r) {
			t.Errorf("M2: served route %s %s is absent from routeManifest", r.method, r.path)
		}
	}
}

// M3: manifest subset-of spec (direction b).
func TestReconcile_ManifestIsDocumented(t *testing.T) {
	spec := routesFromSpec(loadOpenAPISpec(t))
	for _, missing := range diffRoutes(routeManifest, spec) {
		t.Errorf("M3: manifest route %s %s is absent from api/openapi/aero-id-account.yaml", missing.method, missing.path)
	}
}

// M4: spec subset-of served (direction a).
func TestReconcile_DocumentedRoutesAreServed(t *testing.T) {
	spec := routesFromSpec(loadOpenAPISpec(t))
	served := servedRoutes(newFullyWiredRouter(t))
	for _, missing := range documentedButNotServed(spec, served) {
		t.Errorf("direction (a): documented route %s %s is not served by the fully-wired router", missing.method, missing.path)
	}
}

// Direction (b), directly on served vs spec: every served, non-excluded
// control-plane route must be documented. The failure message names the
// missing route.
func TestReconcile_ServedControlPlaneRoutesAreDocumented(t *testing.T) {
	spec := routesFromSpec(loadOpenAPISpec(t))
	served := servedRoutes(newFullyWiredRouter(t))
	for _, missing := range servedButNotDocumented(served, spec) {
		if excludeRoute(missing.path) {
			continue
		}
		t.Errorf("served route %s %s is absent from api/openapi/aero-id-account.yaml", missing.method, missing.path)
	}
}

// TestReconcile_CallbackReplayCanonicalForm (REQ-7) enforces the canonical
// form: spec path+method == served path+method == manifest entry, and the
// spec's success response carries the status the handler actually returns.
//
// Resolution adopted: Option A — the served route is the source of truth
// (live handlers win per AGENTS.md) and the spec was aligned to it: path
// /admin/callback-subscriptions/deliveries/{id}/replay (callback.go:29),
// success 200 (callback.go:82 returns httpx.OK). Option B (rename the
// handler route + return 202) was rejected: it would change shipped behavior
// on an audited surface for no working client, since the documented path
// 404s today and the documented 202 is never returned.
func TestReconcile_CallbackReplayCanonicalForm(t *testing.T) {
	const (
		replayMethod = "POST"
		replayPath   = "/admin/callback-subscriptions/deliveries/{id}/replay"
		replayStatus = "200" // httpx.OK (callback.go:82); handler untouched (Option A)
	)
	spec := loadOpenAPISpec(t)
	served := servedRoutes(newFullyWiredRouter(t))

	// 1. Exactly one manifest entry, in the canonical form.
	var manifestMatches int
	for _, r := range routeManifest {
		if strings.HasPrefix(r.path, "/admin/callback") && strings.Contains(r.path, "/replay") {
			if r.method == replayMethod && r.path == replayPath {
				manifestMatches++
				continue
			}
			t.Errorf("canonical replay: unexpected replay-like manifest entry %s %s", r.method, r.path)
		}
	}
	if manifestMatches != 1 {
		t.Errorf("canonical replay: expected exactly one manifest entry %s %s, found %d", replayMethod, replayPath, manifestMatches)
	}

	// 2. The served route matches the canonical form.
	if !containsRoute(served, route{method: replayMethod, path: replayPath}) {
		t.Errorf("canonical replay: served route %s %s not found (served replay surface drifted)", replayMethod, replayPath)
	}

	// 3. The spec documents the same path+method.
	operations, ok := spec.Paths[replayPath]
	if !ok {
		t.Fatalf("canonical replay: spec path %s missing (documented path drifted)", replayPath)
	}
	op, ok := operations["post"]
	if !ok {
		t.Fatalf("canonical replay: spec method post missing on %s", replayPath)
	}
	if op.OperationID == "" {
		t.Errorf("canonical replay: operationId missing on %s", replayPath)
	}

	// 4. The spec success status equals the status the handler returns.
	if _, ok := op.Responses[replayStatus]; !ok {
		t.Errorf("canonical replay: spec success response is not %s on %s (handler returns httpx.OK = 200 at callback.go:82)", replayStatus, replayPath)
	}
	if _, ok := op.Responses["202"]; ok {
		t.Errorf("canonical replay: spec still documents the never-returned 202 on %s", replayPath)
	}
}

// TestReconcile_CheckerFixtures (REQ-5) is the Go equivalent of the shell
// gate's scripts/testdata/architecture {positive,negative} sweeps: a
// positive fixture that stops matching means the gate went vacuous; a
// negative fixture that starts matching means a rule went over-broad.
// Commenting out any fixture assertion fails this test.
func TestReconcile_CheckerFixtures(t *testing.T) {
	// Canonical aligned pair (negative fixtures must pass).
	aligned := []route{{"GET", "/a"}, {"POST", "/b/{id}"}}
	alignedSpec := []route{{"GET", "/a"}, {"POST", "/b/{id}"}}

	if got := documentedButNotServed(alignedSpec, aligned); len(got) != 0 {
		t.Errorf("negative: documentedButNotServed on aligned pair = %v, want none", got)
	}
	if got := servedButNotDocumented(aligned, alignedSpec); len(got) != 0 {
		t.Errorf("negative: servedButNotDocumented on aligned pair = %v, want none", got)
	}

	// Positive (a): spec documents an unserved route.
	specWithUnserved := append([]route(nil), alignedSpec...)
	specWithUnserved = append(specWithUnserved, route{"DELETE", "/unserved"})
	if got := documentedButNotServed(specWithUnserved, aligned); !containsRoute(got, route{"DELETE", "/unserved"}) {
		t.Error("positive: documentedButNotServed must report DELETE /unserved")
	}

	// Positive (b): served route absent from the spec.
	servedWithExtra := append([]route(nil), aligned...)
	servedWithExtra = append(servedWithExtra, route{"GET", "/undocumented"})
	if got := servedButNotDocumented(servedWithExtra, alignedSpec); !containsRoute(got, route{"GET", "/undocumented"}) {
		t.Error("positive: servedButNotDocumented must report GET /undocumented")
	}

	// Positive (c): served set missing one manifest entry -> M1 violation.
	servedMissing := []route{{"GET", "/a"}}
	if missing := diffRoutes(aligned, servedMissing); !containsRoute(missing, route{"POST", "/b/{id}"}) {
		t.Error("positive: M1 diff must report POST /b/{id} missing from served")
	}
	// Negative: full manifest served -> nothing missing.
	if missing := diffRoutes(aligned, aligned); len(missing) != 0 {
		t.Errorf("negative: M1 diff on aligned pair = %v, want none", missing)
	}

	// containsRoute positive/negative (M2's membership primitive).
	if !containsRoute(aligned, route{"GET", "/a"}) {
		t.Error("positive: containsRoute must be true for a route present in the set")
	}
	if containsRoute(aligned, route{"GET", "/undocumented"}) {
		t.Error("negative: containsRoute must be false for a route absent from the set")
	}

	// normalizeRoute positive.
	if got, want := normalizeRoute("/admin/accounts/:id"), "/admin/accounts/{id}"; got != want {
		t.Errorf("normalizeRoute: got %q want %q", got, want)
	}
	if got, want := normalizeRoute("/admin/audit/holds/:scope_type/:scope_id"), "/admin/audit/holds/{scope_type}/{scope_id}"; got != want {
		t.Errorf("normalizeRoute: got %q want %q", got, want)
	}
	// normalizeRoute negative: literal segments untouched, params preserved.
	if got, want := normalizeRoute("/admin/callback-subscriptions/deliveries/:id/replay"), "/admin/callback-subscriptions/deliveries/{id}/replay"; got != want {
		t.Errorf("normalizeRoute: got %q want %q", got, want)
	}
	if got := normalizeRoute("/health/sources"); got != "/health/sources" {
		t.Errorf("normalizeRoute negative: literal path mutated to %q", got)
	}

	// excludeRoute positive: every declared prefix excludes itself and
	// descendants.
	for _, p := range declaredExclusionSet {
		if !excludeRoute(p) {
			t.Errorf("positive: excludeRoute(%s) must be true", p)
		}
		if !excludeRoute(p + "/child") {
			t.Errorf("positive: excludeRoute(%s/child) must be true", p)
		}
	}
	// excludeRoute negative: control-plane surface never excluded.
	if excludeRoute("/me/resources") || excludeRoute("/admin/accounts") || excludeRoute("/audit/verify") {
		t.Error("negative: excludeRoute must not match control-plane routes")
	}
}

// TestReconcile_FullyWiredRouterConditionals (REQ-8) proves the test router
// carries every conditional surface of the deployment-maximum wiring, so
// direction (a) verifies the documented conditional routes against the router.
func TestReconcile_FullyWiredRouterConditionals(t *testing.T) {
	conditionals := []route{
		{method: "POST", path: "/admin/regions/promote"},
		{method: "GET", path: "/admin/regions/directory/{account_id}"},
		{method: "GET", path: "/admin/audit/holds"},
		{method: "POST", path: "/admin/audit/holds"},
		{method: "DELETE", path: "/admin/audit/holds/{scope_type}/{scope_id}"},
		{method: "POST", path: "/admin/audit/anchor"},
		{method: "GET", path: "/admin/audit/anchor/manifest/{source}/{partition}"},
		{method: "POST", path: "/audit/export"},
		{method: "POST", path: "/audit/replays/projection"},
		{method: "POST", path: "/admin/migration/legacy/compare"},
		{method: "GET", path: "/admin/migration/legacy/cutover/{tenant_id}"},
		{method: "POST", path: "/admin/migration/legacy/cutover"},
		{method: "GET", path: "/admin/migration/legacy/cleanup-readiness/{tenant_id}"},
		{method: "POST", path: "/admin/bans"},
		{method: "GET", path: "/admin/bans"},
		{method: "DELETE", path: "/admin/bans/{uid}"},
		{method: "GET", path: "/files/dl"},
	}
	served := servedRoutes(newFullyWiredRouter(t))
	for _, missing := range diffRoutes(conditionals, served) {
		t.Errorf("fully-wired router missing conditional route %s %s", missing.method, missing.path)
	}
}

// TestExclusionSetIsPinned (REQ-5/REQ-6) pins the active exclusion set to the
// declared const exactly and requires a written justification for every
// non-default entry — widening exclusions to silence drift requires a
// deliberate test edit (the "negative fixture that starts matching" class).
func TestExclusionSetIsPinned(t *testing.T) {
	got := make([]string, len(excludedRoutePrefixes))
	for i, p := range excludedRoutePrefixes {
		got[i] = p.prefix
	}
	if !slices.Equal(got, declaredExclusionSet) {
		t.Fatalf("exclusion set drifted:\n got: %v\nwant: %v", got, declaredExclusionSet)
	}
	for _, p := range excludedRoutePrefixes[len(defaultExclusionPrefixes):] {
		if strings.TrimSpace(p.justification) == "" {
			t.Errorf("justified exclusion %q has no written justification (REQ-6)", p.prefix)
		}
	}
	// No control-plane route may match an exclusion.
	for _, r := range routeManifest {
		if excludeRoute(r.path) {
			t.Errorf("control-plane route %s %s matches an exclusion prefix", r.method, r.path)
		}
	}
}
