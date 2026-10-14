package satellite

import "onboardingportal/models"

const (
	RoleEntitledParty   = "EntitledParty"
	RoleServiceConsumer = "ServiceConsumer"
	RoleServiceProvider = "ServiceProvider"
)

// FrameworkRolesFromProposal maps the roles chosen during onboarding to framework
// role ids, or returns fallbackRole when none were chosen.
func FrameworkRolesFromProposal(proposal *models.Proposal, fallbackRole string) []string {
	roles := make([]string, 0, 3)
	if proposal.DataOwner {
		roles = append(roles, RoleEntitledParty)
	}
	if proposal.DataConsumer {
		roles = append(roles, RoleServiceConsumer)
	}
	if proposal.DataProvider {
		roles = append(roles, RoleServiceProvider)
	}
	if len(roles) == 0 {
		return []string{fallbackRole}
	}
	return roles
}

// rolesWithoutCertificate replaces roles the satellite only grants to parties with an
// x509 certificate (anything but ServiceConsumer/EntitledParty) by EntitledParty.
func rolesWithoutCertificate(roles []string) []string {
	result := make([]string, 0, len(roles))
	seen := map[string]bool{}
	for _, role := range roles {
		if role != RoleServiceConsumer {
			role = RoleEntitledParty
		}
		if !seen[role] {
			seen[role] = true
			result = append(result, role)
		}
	}
	return result
}
