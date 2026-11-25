package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRBACRolesAndAppScoping(t *testing.T) {
	authz, err := NewRBAC([]KeySpec{
		{Key: "k-ingest", Actor: "mobile-sdk", Role: RoleIngestOnly, AppIDs: []string{"dev.caliper.demo"}},
		{Key: "k-viewer", Actor: "pm-alice", Role: RoleViewer},
		{Key: "k-operator", Actor: "oncall-bob", Role: RoleOperator, AppIDs: []string{"dev.caliper.demo"}},
		{Key: "k-admin", Actor: "sec-carol", Role: RoleAdmin},
	})
	if err != nil {
		t.Fatalf("new rbac: %v", err)
	}

	reqWithKey := func(k string, bearer bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if k != "" {
			if bearer {
				r.Header.Set("Authorization", "Bearer "+k)
			} else {
				r.Header.Set("X-Caliper-Key", k)
			}
		}
		return r
	}

	// Missing key -> 401
	if _, status, _, _ := authz.Authorize(reqWithKey("", false), PermReadAnalytics, ""); status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", status)
	}

	// ingest_only can ingest allowed app, cannot ingest other app, cannot read analytics
	if _, status, _, _ := authz.Authorize(reqWithKey("k-ingest", false), PermIngest, "dev.caliper.demo"); status != 0 {
		t.Fatalf("expected ingest allowed, got %d", status)
	}
	if _, status, _, _ := authz.Authorize(reqWithKey("k-ingest", false), PermIngest, "dev.other.app"); status != http.StatusForbidden {
		t.Fatalf("expected 403 for out-of-scope app, got %d", status)
	}
	if _, status, _, _ := authz.Authorize(reqWithKey("k-ingest", false), PermReadAnalytics, "dev.caliper.demo"); status != http.StatusForbidden {
		t.Fatalf("expected 403 for ingest_only reading analytics, got %d", status)
	}

	// viewer can read analytics, CANNOT do audited session lookup or read audit logs
	if _, status, _, _ := authz.Authorize(reqWithKey("k-viewer", true), PermReadAnalytics, "dev.caliper.demo"); status != 0 {
		t.Fatalf("expected viewer read allowed, got %d", status)
	}
	if _, status, _, _ := authz.Authorize(reqWithKey("k-viewer", true), PermSessionLookup, "dev.caliper.demo"); status != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer session lookup, got %d", status)
	}

	// operator can do session lookup, CANNOT read audit logs
	if p, status, _, _ := authz.Authorize(reqWithKey("k-operator", true), PermSessionLookup, "dev.caliper.demo"); status != 0 || p.Actor != "oncall-bob" {
		t.Fatalf("expected operator session lookup allowed, got status=%d actor=%s", status, p.Actor)
	}
	if _, status, _, _ := authz.Authorize(reqWithKey("k-operator", true), PermReadAuditLogs, ""); status != http.StatusForbidden {
		t.Fatalf("expected 403 for operator reading audit logs, got %d", status)
	}

	// admin can read audit logs
	if p, status, _, _ := authz.Authorize(reqWithKey("k-admin", false), PermReadAuditLogs, ""); status != 0 || p.Actor != "sec-carol" {
		t.Fatalf("expected admin audit logs allowed, got status=%d actor=%s", status, p.Actor)
	}
}

func TestAuthorizeScopedTenantIsolation(t *testing.T) {
	authz, err := NewRBAC([]KeySpec{
		{Key: "k-single", Actor: "tenant-a", Role: RoleViewer, AppIDs: []string{"app.alpha"}},
		{Key: "k-multi", Actor: "tenant-ab", Role: RoleViewer, AppIDs: []string{"app.alpha", "app.beta"}},
		{Key: "k-global", Actor: "global-viewer", Role: RoleViewer},
	})
	if err != nil {
		t.Fatalf("new rbac: %v", err)
	}
	req := func(k string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/mcp", nil)
		r.Header.Set("X-Caliper-Key", k)
		return r
	}

	// Single-app scoped key omitting app_id auto-scopes to app.alpha.
	if _, appID, status, _, _ := authz.AuthorizeScoped(req("k-single"), PermReadAnalytics, ""); status != 0 || appID != "app.alpha" {
		t.Fatalf("expected single-app auto-scope to app.alpha, got status=%d appID=%q", status, appID)
	}
	// Single-app scoped key requesting another app -> 403 forbidden_app_scope.
	if _, _, status, code, _ := authz.AuthorizeScoped(req("k-single"), PermReadAnalytics, "app.beta"); status != http.StatusForbidden || code != "forbidden_app_scope" {
		t.Fatalf("expected 403 forbidden_app_scope, got status=%d code=%q", status, code)
	}
	// Multi-app scoped key omitting app_id -> 403 (no wildcard leak across tenants).
	if _, _, status, code, _ := authz.AuthorizeScoped(req("k-multi"), PermReadAnalytics, ""); status != http.StatusForbidden || code != "forbidden_app_scope" {
		t.Fatalf("expected 403 for multi-app empty app_id, got status=%d code=%q", status, code)
	}
	// Multi-app scoped key specifying authorized app -> allowed.
	if _, appID, status, _, _ := authz.AuthorizeScoped(req("k-multi"), PermReadAnalytics, "app.beta"); status != 0 || appID != "app.beta" {
		t.Fatalf("expected app.beta allowed, got status=%d appID=%q", status, appID)
	}
	// Global unscoped key omitting app_id -> allowed with empty wildcard.
	if _, appID, status, _, _ := authz.AuthorizeScoped(req("k-global"), PermReadAnalytics, ""); status != 0 || appID != "" {
		t.Fatalf("expected global empty app_id allowed, got status=%d appID=%q", status, appID)
	}
}
