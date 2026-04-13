package main

import (
	"fmt"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHasRole(t *testing.T) {
	tests := []struct {
		name   string
		roles  []model.Role
		target model.Role
		want   bool
	}{
		{"owner in [owner]", []model.Role{model.RoleOwner}, model.RoleOwner, true},
		{"member in [member]", []model.Role{model.RoleMember}, model.RoleMember, true},
		{"owner not in [member]", []model.Role{model.RoleMember}, model.RoleOwner, false},
		{"member not in [owner]", []model.Role{model.RoleOwner}, model.RoleMember, false},
		{"empty roles", []model.Role{}, model.RoleOwner, false},
		{"nil roles", nil, model.RoleOwner, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasRole(tt.roles, tt.target)
			if got != tt.want {
				t.Errorf("HasRole(%v, %q) = %v, want %v", tt.roles, tt.target, got, tt.want)
			}
		})
	}
}

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"invalid role: must be owner or member", "invalid role: must be owner or member"},
		{"only owners can update roles", "only owners can update roles"},
		{"cannot demote the last owner", "cannot demote the last owner"},
		{"some internal db error", "internal error"},
		{"mongo timeout", "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeError(fmt.Errorf("%s", tt.input))
			if got != tt.want {
				t.Errorf("sanitizeError(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
