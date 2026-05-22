// Package domain holds the identity context's entities and value objects.
//
// Rules:
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Errors returned here use pkg/errors.
//   - Constructors enforce invariants — callers can't get an invalid User.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// BranchLevel describes which level of the hierarchy a branch sits at.
// Used to scope a user to a specific branch level (regional/area/sub_area).
type BranchLevel string

const (
	BranchLevelRegional BranchLevel = "regional"
	BranchLevelArea     BranchLevel = "area"
	BranchLevelSubArea  BranchLevel = "sub_area"
)

func (b BranchLevel) Valid() bool {
	switch b {
	case BranchLevelRegional, BranchLevelArea, BranchLevelSubArea:
		return true
	}
	return false
}

// User is the identity aggregate root.
type User struct {
	ID            uuid.UUID
	EmployeeID    string
	FullName      string
	Email         string
	Phone         string
	PasswordHash  string
	ReportsToID   *uuid.UUID // direct manager / supervisor
	BranchID      *uuid.UUID
	BranchLevel   *BranchLevel
	Active        bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewUser constructs a User. Performs the invariants we want to keep true
// for every user record everywhere.
func NewUser(employeeID, fullName, email, phone, passwordHash string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	fullName = strings.TrimSpace(fullName)

	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.Validation("user.email_invalid", "email is invalid")
	}
	if fullName == "" {
		return nil, errors.Validation("user.name_required", "full name is required")
	}
	if passwordHash == "" {
		return nil, errors.Validation("user.password_required", "password is required")
	}

	now := time.Now().UTC()
	return &User{
		ID:           uuid.New(),
		EmployeeID:   strings.TrimSpace(employeeID),
		FullName:     fullName,
		Email:        email,
		Phone:        strings.TrimSpace(phone),
		PasswordHash: passwordHash,
		Active:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// AssignBranch attaches a branch and level to the user. Both or neither.
func (u *User) AssignBranch(branchID uuid.UUID, level BranchLevel) error {
	if !level.Valid() {
		return errors.Validation("user.branch_level_invalid", "invalid branch level")
	}
	u.BranchID = &branchID
	u.BranchLevel = &level
	u.UpdatedAt = time.Now().UTC()
	return nil
}

// Deactivate marks the user inactive. Inactive users cannot log in but their
// records (commissions, audit, BAST signatures) remain intact for compliance.
func (u *User) Deactivate() {
	u.Active = false
	u.UpdatedAt = time.Now().UTC()
}

// SalesType narrows which pipelines a sales rep can work, per CRM §3.1.
type SalesType string

const (
	SalesTypeBroadband  SalesType = "broadband"
	SalesTypeEnterprise SalesType = "enterprise"
	SalesTypeBoth       SalesType = "both"
)

func (s SalesType) Valid() bool {
	switch s {
	case SalesTypeBroadband, SalesTypeEnterprise, SalesTypeBoth:
		return true
	}
	return false
}

// TechnicianGrade — senior leads, junior assists. PRD §4.3 mandates
// every team has at least one Senior.
type TechnicianGrade string

const (
	TechnicianGradeSenior TechnicianGrade = "senior"
	TechnicianGradeJunior TechnicianGrade = "junior"
)

func (g TechnicianGrade) Valid() bool {
	return g == TechnicianGradeSenior || g == TechnicianGradeJunior
}
