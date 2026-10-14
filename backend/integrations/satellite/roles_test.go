package satellite

import (
	"crypto/x509/pkix"
	"onboardingportal/models"
	"reflect"
	"testing"
)

func TestFrameworkRolesFromProposal(t *testing.T) {
	cases := []struct {
		name     string
		proposal models.Proposal
		want     []string
	}{
		{"owner", models.Proposal{DataOwner: true}, []string{RoleEntitledParty}},
		{"consumer", models.Proposal{DataConsumer: true}, []string{RoleServiceConsumer}},
		{"provider", models.Proposal{DataProvider: true}, []string{RoleServiceProvider}},
		{
			"all",
			models.Proposal{DataOwner: true, DataConsumer: true, DataProvider: true},
			[]string{RoleEntitledParty, RoleServiceConsumer, RoleServiceProvider},
		},
		{"none falls back", models.Proposal{}, []string{"FallbackRole"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FrameworkRolesFromProposal(&tc.proposal, "FallbackRole")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func frameworkRoleIDs(claims []map[string]interface{}) []string {
	var roles []string
	for _, claim := range claims {
		if claim["type"] == "frameworkRole" {
			roles = append(roles, claim["roleId"].(string))
		}
	}
	return roles
}

func TestBuildV3OnboardingClaimsEmitsOneRoleClaimPerChosenRole(t *testing.T) {
	x5c, _ := testCertificateX5C(t, pkix.Name{CommonName: "Participant"})

	claims, err := BuildV3OnboardingClaims(&models.Proposal{
		DataConsumer: true,
		DataProvider: true,
		CertX5c:      x5c,
	}, minimalV3ClaimConfig())
	if err != nil {
		t.Fatalf("BuildV3OnboardingClaims returned error: %v", err)
	}

	want := []string{RoleServiceConsumer, RoleServiceProvider}
	if got := frameworkRoleIDs(claims); !reflect.DeepEqual(got, want) {
		t.Fatalf("got roles %v, want %v", got, want)
	}
}

func TestBuildV3OnboardingClaimsDowngradesCertificateRolesWithoutCertificate(t *testing.T) {
	claims, err := BuildV3OnboardingClaims(&models.Proposal{
		DataOwner:    true,
		DataConsumer: true,
		DataProvider: true,
		IdpAssertion: "assertion",
	}, minimalV3ClaimConfig())
	if err != nil {
		t.Fatalf("BuildV3OnboardingClaims returned error: %v", err)
	}

	want := []string{RoleEntitledParty, RoleServiceConsumer}
	if got := frameworkRoleIDs(claims); !reflect.DeepEqual(got, want) {
		t.Fatalf("got roles %v, want %v", got, want)
	}
}

func TestBuildParty22RequestUsesChosenRoles(t *testing.T) {
	payload, err := BuildParty22RequestFromProposal(
		&models.Proposal{DataConsumer: true, DataProvider: true, IdpAssertion: "assertion"},
		"did:ishare:EU.NL.NTRNL-12345678", nil, "EU.EORI.NL.REGISTRAR", "", "", "", nil,
		"2026-01-01T00:00:00Z", "2027-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("BuildParty22RequestFromProposal returned error: %v", err)
	}

	var got []string
	for _, role := range payload.Roles {
		got = append(got, role.Role)
	}
	want := []string{RoleServiceConsumer, RoleServiceProvider}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got roles %v, want %v", got, want)
	}
}
