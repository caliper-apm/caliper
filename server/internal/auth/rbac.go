package auth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Role defines least-privilege access tiers for Caliper APM endpoints.
type Role string

const (
	RoleIngestOnly Role = "ingest_only"
	RoleViewer     Role = "viewer"
	RoleOperator   Role = "operator"
	RoleAdmin      Role = "admin"
)

// Permission identifies one guarded capability.
type Permission string

const (
	PermIngest        Permission = "ingest"
	PermReadAnalytics Permission = "read_analytics"
	PermWriteRelease  Permission = "write_release"
	PermManageAlerts  Permission = "manage_alerts"
	PermSessionLookup Permission = "session_lookup"
	PermReadAuditLogs Permission = "read_audit_logs"
)

// KeySpec configures one static API key or bearer token entry in RBAC mode.
type KeySpec struct {
	Key    string   `json:"key"`
	Actor  string   `json:"actor"`
	Role   Role     `json:"role"`
	AppIDs []string `json:"app_ids,omitempty"` // empty = all apps
}

// ConfigFile is the JSON structure loaded by --auth-config.
type ConfigFile struct {
	Keys []KeySpec `json:"keys"`
}

// Principal represents the authenticated caller attached to a request.
type Principal struct {
	Actor  string   `json:"actor"`
	Role   Role     `json:"role"`
	AppIDs []string `json:"app_ids,omitempty"`
	Mode   string   `json:"mode"` // "rbac" | "single_api_key" | "dev_open"
}

// Authorizer validates API credentials and enforces RBAC + workspace app scoping.
type Authorizer struct {
	mode         string
	singleAPIKey string
	keys         []KeySpec
}

// NewOpenDev creates a local-dev authorizer where callers receive admin role (audit logs still enforced).
func NewOpenDev() *Authorizer {
	return &Authorizer{mode: "dev_open"}
}

// NewSingleAPIKey preserves v0.1 behavior: writes & privileged ops require apiKey; read analytics stay open.
func NewSingleAPIKey(apiKey string) *Authorizer {
	if strings.TrimSpace(apiKey) == "" {
		return NewOpenDev()
	}
	return &Authorizer{mode: "single_api_key", singleAPIKey: apiKey}
}

// NewRBAC creates a strict RBAC authorizer enforcing credentials across all API permissions.
func NewRBAC(keys []KeySpec) (*Authorizer, error) {
	if len(keys) == 0 {
		return nil, errors.New("rbac config requires at least one key")
	}
	cleaned := make([]KeySpec, 0, len(keys))
	for i, k := range keys {
		if strings.TrimSpace(k.Key) == "" {
			return nil, fmt.Errorf("rbac key[%d]: empty key", i)
		}
		if strings.TrimSpace(k.Actor) == "" {
			return nil, fmt.Errorf("rbac key[%d]: empty actor", i)
		}
		if !isValidRole(k.Role) {
			return nil, fmt.Errorf("rbac key[%d]: invalid role %q", i, k.Role)
		}
		cleaned = append(cleaned, k)
	}
	return &Authorizer{mode: "rbac", keys: cleaned}, nil
}

// LoadRBACFile loads an Authorizer from a JSON config file.
func LoadRBACFile(path string) (*Authorizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read auth config: %w", err)
	}
	var cfg ConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse auth config: %w", err)
	}
	return NewRBAC(cfg.Keys)
}

func isValidRole(r Role) bool {
	switch r {
	case RoleIngestOnly, RoleViewer, RoleOperator, RoleAdmin:
		return true
	default:
		return false
	}
}

// RoleAllows reports whether role grants permission perm.
func RoleAllows(role Role, perm Permission) bool {
	switch role {
	case RoleAdmin:
		return true
	case RoleOperator:
		return perm != PermReadAuditLogs
	case RoleViewer:
		return perm == PermReadAnalytics
	case RoleIngestOnly:
		return perm == PermIngest
	default:
		return false
	}
}

// ExtractToken reads X-Caliper-Key or Authorization: Bearer <token>.
func ExtractToken(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-Caliper-Key")); k != "" {
		return k
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authz) > 7 && strings.EqualFold(authz[:7], "Bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

// Authenticate resolves the caller Principal for r, or returns (Principal{}, false) when invalid/missing in RBAC mode.
func (a *Authorizer) Authenticate(r *http.Request) (Principal, bool) {
	if a == nil || a.mode == "dev_open" {
		return Principal{Actor: "local-dev", Role: RoleAdmin, Mode: "dev_open"}, true
	}
	token := ExtractToken(r)

	if a.mode == "single_api_key" {
		if token != "" && constantTimeEqual(token, a.singleAPIKey) {
			return Principal{Actor: "api-key-admin", Role: RoleAdmin, Mode: "single_api_key"}, true
		}
		if token == "" {
			return Principal{Actor: "anonymous-viewer", Role: RoleViewer, Mode: "single_api_key"}, true
		}
		return Principal{}, false
	}

	// Strict RBAC mode
	if token == "" {
		return Principal{}, false
	}
	for _, spec := range a.keys {
		if constantTimeEqual(token, spec.Key) {
			return Principal{
				Actor:  spec.Actor,
				Role:   spec.Role,
				AppIDs: spec.AppIDs,
				Mode:   "rbac",
			}, true
		}
	}
	return Principal{}, false
}

// Authorize checks authentication, role permission, and optional app_id workspace scope.
// Returns (principal, httpStatus, errorCode, errorMessage). httpStatus == 0 means allowed.
func (a *Authorizer) Authorize(r *http.Request, perm Permission, appID string) (Principal, int, string, string) {
	p, ok := a.Authenticate(r)
	if !ok {
		return Principal{}, http.StatusUnauthorized, "unauthorized", "missing or invalid credentials"
	}
	if !RoleAllows(p.Role, perm) {
		if a.mode == "single_api_key" && p.Actor == "anonymous-viewer" {
			return Principal{}, http.StatusUnauthorized, "unauthorized", "missing or invalid X-Caliper-Key"
		}
		return Principal{}, http.StatusForbidden, "forbidden", fmt.Sprintf("role %q lacks permission %q", p.Role, perm)
	}
	if appID != "" && len(p.AppIDs) > 0 && !containsString(p.AppIDs, appID) {
		return Principal{}, http.StatusForbidden, "forbidden_app_scope", fmt.Sprintf("actor %q is not authorized for app_id %q", p.Actor, appID)
	}
	return p, 0, "", ""
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
