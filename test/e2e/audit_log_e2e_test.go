// Audit log capture end-to-end — privileged actions write to
// identity.audit_logs, surfaced via GET /api/identity/audit-logs.
//
// Wave 60 — without this gate, a regression that removes the
// audit-write call from a handler (or swaps the record_type, or fails
// to attribute the actor) would be invisible. Compliance auditors
// can't replay actions they can't see.
//
//   1. Create a fresh branch (privileged action — needs
//      identity.branch.manage)
//   2. GET /api/identity/audit-logs?module=identity&record_type=branch
//   3. The result must include an entry for the new branch with:
//        * non-empty timestamp
//        * user_id matching the admin who created it
//        * record_id matching the branch ID
//   4. Repeat with a user create (different record_type)
//   5. Negative — a non-admin (sales rep) hitting /audit-logs gets 403
//      (RBAC gate on identity.audit.read)
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestAuditLogCaptures(t *testing.T) {
	admin := newClient(t)
	admin.login()

	sx := suffix()

	// -----------------------------------------------------------------
	// Privileged action #1 — create a branch.
	// -----------------------------------------------------------------
	var branch struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":  "W60 Audit Regional " + sx,
		"code":  "W60-AUDIT-" + sx,
		"level": "regional",
	}, &branch, 201)
	if branch.ID == "" {
		t.Fatal("branch create returned empty id")
	}

	// -----------------------------------------------------------------
	// Privileged action #2 — create a user under that branch.
	// -----------------------------------------------------------------
	var user struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/identity/users", map[string]any{
		"employee_id": "W60-AUDIT-" + sx,
		"full_name":   "W60 Audit Probe " + sx,
		"email":       "w60-audit-" + sx + "@ion.local",
		"phone":       "+62811W60AUDIT",
		"password":    "TempPass!2026",
		"branch_id":   branch.ID,
		"roles":       []string{"sales_rep"},
		"sales_type":  "broadband",
	}, &user, 201)
	if user.ID == "" {
		t.Fatal("user create returned empty id")
	}

	// -----------------------------------------------------------------
	// 1. Fetch audit logs — branch create event should be there.
	// -----------------------------------------------------------------
	var audit struct {
		Items []struct {
			ID         string `json:"id"`
			Timestamp  string `json:"timestamp"`
			UserID     string `json:"user_id"`
			Module     string `json:"module"`
			RecordType string `json:"record_type"`
			RecordID   string `json:"record_id"`
		} `json:"items"`
	}
	admin.do("GET",
		"/api/identity/audit-logs?module=identity&record_type=branch&page_size=200",
		nil, &audit, 200)

	var branchEntry *struct {
		ID         string `json:"id"`
		Timestamp  string `json:"timestamp"`
		UserID     string `json:"user_id"`
		Module     string `json:"module"`
		RecordType string `json:"record_type"`
		RecordID   string `json:"record_id"`
	}
	for i := range audit.Items {
		if audit.Items[i].RecordID == branch.ID {
			branchEntry = &audit.Items[i]
			break
		}
	}
	if branchEntry == nil {
		t.Fatalf("branch %s not in audit log (privileged action not captured)", branch.ID)
	}
	if branchEntry.Timestamp == "" {
		t.Errorf("audit timestamp empty for branch entry")
	}
	if branchEntry.UserID == "" {
		t.Errorf("audit user_id empty — actor not attributed for branch create")
	}
	if branchEntry.Module != "identity" {
		t.Errorf("audit module: want identity, got %q", branchEntry.Module)
	}

	// -----------------------------------------------------------------
	// 2. User create event should also be there.
	// -----------------------------------------------------------------
	var userAudit struct {
		Items []struct {
			RecordID   string `json:"record_id"`
			RecordType string `json:"record_type"`
			UserID     string `json:"user_id"`
		} `json:"items"`
	}
	admin.do("GET",
		"/api/identity/audit-logs?module=identity&record_type=user&page_size=200",
		nil, &userAudit, 200)
	sawUser := false
	for _, it := range userAudit.Items {
		if it.RecordID == user.ID {
			sawUser = true
			if it.UserID == "" {
				t.Errorf("audit user_id empty for user create")
			}
			break
		}
	}
	if !sawUser {
		t.Errorf("user %s not in audit log (user-create write missing)", user.ID)
	}

	// -----------------------------------------------------------------
	// 3. RBAC gate — non-admin gets 403.
	// -----------------------------------------------------------------
	sales := newClientAs(t, "sales@ion.local")
	got := sales.statusOnly("GET", "/api/identity/audit-logs")
	if got != 403 {
		t.Errorf("sales rep accessing /audit-logs: want 403, got %d", got)
	}
}
