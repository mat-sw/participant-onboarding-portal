package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"onboardingportal/config"
	"onboardingportal/models"
	s "onboardingportal/server"
	"onboardingportal/server/middlewares"
)

const (
	ownPartyID   = "did:ishare:EU.NL.NTRNL-11111111"
	otherPartyID = "did:ishare:EU.NL.NTRNL-22222222"
)

type patchedClaim struct {
	partyID, claimID string
	body             map[string]interface{}
}

type myPartyTestEnv struct {
	app     *fiber.App
	db      *gorm.DB
	patched []patchedClaim
	parties map[string]map[string]interface{}
}

func complianceParty(id string, claims ...map[string]interface{}) map[string]interface{} {
	list := make([]interface{}, 0, len(claims))
	for _, c := range claims {
		list = append(list, c)
	}
	return map[string]interface{}{"id": id, "claims": list}
}

func claim(id, claimType, status string) map[string]interface{} {
	return map[string]interface{}{"id": id, "type": claimType, "status": status}
}

func newMyPartyTestEnv(t *testing.T, username string) *myPartyTestEnv {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := database.AutoMigrate(&models.Proposal{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	env := &myPartyTestEnv{
		db: database,
		parties: map[string]map[string]interface{}{
			ownPartyID:   complianceParty(ownPartyID, claim("own-fc", "frameworkCompliance", "active"), claim("own-role", "frameworkRole", "active")),
			otherPartyID: complianceParty(otherPartyID, claim("other-fc", "frameworkCompliance", "active")),
		},
	}
	handler := &HandlerMyParty{
		Server: &s.Server{DB: database},
		Config: &config.Config{SatelliteVersion: "3.0"},
		fetchParty: func(id string) (map[string]interface{}, error) {
			return env.parties[id], nil
		},
		patchClaim: func(c *fiber.Ctx, partyID, claimID string, body []byte) error {
			var decoded map[string]interface{}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("forwarded body is not JSON: %v", err)
			}
			env.patched = append(env.patched, patchedClaim{partyID, claimID, decoded})
			return c.JSON(fiber.Map{"ok": true})
		},
	}

	env.app = fiber.New()
	env.app.Use(func(c *fiber.Ctx) error {
		if username != "" {
			c.Locals("claims", &middlewares.KeycloakClaims{PreferredUsername: username})
		}
		return c.Next()
	})
	env.app.Patch("/registry/me/party/compliance", handler.PatchMyCompliance)
	return env
}

func (env *myPartyTestEnv) addProposal(t *testing.T, username, partyID, status string) {
	t.Helper()
	if err := env.db.Create(&models.Proposal{KeycloakUsername: username, PartyId: partyID, Status: status}).Error; err != nil {
		t.Fatalf("create proposal: %v", err)
	}
}

func (env *myPartyTestEnv) patch(t *testing.T, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/registry/me/party/compliance", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := env.app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func TestPatchMyComplianceUpdatesOnlyTheCallersOwnClaim(t *testing.T) {
	env := newMyPartyTestEnv(t, "alice")
	env.addProposal(t, "alice", ownPartyID, "completed")
	env.addProposal(t, "bob", otherPartyID, "completed")

	status, body := env.patch(t, `{"website":" https://example.org ","companyEmail":"info@example.org","tags":"connector","description":"line 1\nline 2"}`)
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if len(env.patched) != 1 {
		t.Fatalf("expected one forwarded PATCH, got %d", len(env.patched))
	}
	got := env.patched[0]
	if got.partyID != ownPartyID || got.claimID != "own-fc" {
		t.Fatalf("patched %s/%s, want %s/own-fc", got.partyID, got.claimID, ownPartyID)
	}
	want := map[string]interface{}{
		"additionalInfo.website":      "https://example.org",
		"additionalInfo.companyEmail": "info@example.org",
		"additionalInfo.tags":         "connector",
		"additionalInfo.description":  "line 1\nline 2",
	}
	if !reflect.DeepEqual(got.body, want) {
		t.Fatalf("forwarded body %v, want %v", got.body, want)
	}
}

func TestPatchMyComplianceRejectsFieldsOutsideTheAllowList(t *testing.T) {
	for _, body := range []string{
		`{"status":"revoked"}`,
		`{"website":"https://example.org","endDate":"2099-01-01"}`,
		`{"additionalInfo.publiclyPublishable":true}`,
		`{"partyId":"` + otherPartyID + `","website":"https://example.org"}`,
		`{"claimId":"other-fc","website":"https://example.org"}`,
		`{"website":"https://example.org"}{"status":"revoked"}`,
		`{}`,
		`not json`,
	} {
		t.Run(body, func(t *testing.T) {
			env := newMyPartyTestEnv(t, "alice")
			env.addProposal(t, "alice", ownPartyID, "completed")
			if status, resp := env.patch(t, body); status != 400 {
				t.Fatalf("status=%d body=%s, want 400", status, resp)
			}
			if len(env.patched) != 0 {
				t.Fatalf("nothing may be forwarded, got %v", env.patched)
			}
		})
	}
}

func TestPatchMyComplianceValidatesValues(t *testing.T) {
	for _, body := range []string{
		`{"capabilityUrl":"javascript:alert(1)"}`,
		`{"capabilityUrl":"https://user:pass@example.org"}`,
		`{"website":"example.org"}`,
		`{"companyEmail":"Info <info@example.org>"}`,
		`{"companyEmail":"not-an-email"}`,
		`{"companyPhone":"call me"}`,
		`{"tags":"a\u0000b"}`,
		`{"tags":"` + strings.Repeat("x", 513) + `"}`,
	} {
		t.Run(body, func(t *testing.T) {
			env := newMyPartyTestEnv(t, "alice")
			env.addProposal(t, "alice", ownPartyID, "completed")
			if status, resp := env.patch(t, body); status != 400 {
				t.Fatalf("status=%d body=%s, want 400", status, resp)
			}
			if len(env.patched) != 0 {
				t.Fatalf("nothing may be forwarded, got %v", env.patched)
			}
		})
	}
}

func TestPatchMyComplianceAllowsClearingAField(t *testing.T) {
	env := newMyPartyTestEnv(t, "alice")
	env.addProposal(t, "alice", ownPartyID, "completed")

	if status, body := env.patch(t, `{"companyPhone":""}`); status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if v := env.patched[0].body["additionalInfo.companyPhone"]; v != "" {
		t.Fatalf("companyPhone = %v, want empty", v)
	}
}

func TestPatchMyComplianceIgnoresProposalsThatAreNotCompleted(t *testing.T) {
	// A rejected proposal can be modified by its owner, and its party id is
	// applicant input, so it must never select the party to edit.
	for _, status := range []string{"pending", "approved", "signed", "rejected"} {
		t.Run(status, func(t *testing.T) {
			env := newMyPartyTestEnv(t, "mallory")
			env.addProposal(t, "mallory", otherPartyID, status)
			env.addProposal(t, "bob", otherPartyID, "completed")
			if code, resp := env.patch(t, `{"website":"https://evil.example"}`); code != 404 {
				t.Fatalf("status=%d body=%s, want 404", code, resp)
			}
			if len(env.patched) != 0 {
				t.Fatalf("nothing may be forwarded, got %v", env.patched)
			}
		})
	}
}

func TestPatchMyComplianceRejectsAmbiguousOwnership(t *testing.T) {
	env := newMyPartyTestEnv(t, "alice")
	env.addProposal(t, "alice", ownPartyID, "completed")
	env.addProposal(t, "alice", otherPartyID, "completed")

	if status, body := env.patch(t, `{"website":"https://example.org"}`); status != 409 {
		t.Fatalf("status=%d body=%s, want 409", status, body)
	}
	if len(env.patched) != 0 {
		t.Fatalf("nothing may be forwarded, got %v", env.patched)
	}
}

func TestPatchMyComplianceRequiresAnAuthenticatedUser(t *testing.T) {
	env := newMyPartyTestEnv(t, "")
	env.addProposal(t, "", ownPartyID, "completed")

	if status, body := env.patch(t, `{"website":"https://example.org"}`); status != 401 {
		t.Fatalf("status=%d body=%s, want 401", status, body)
	}
}

func TestPatchMyComplianceRequiresAV3Satellite(t *testing.T) {
	env := newMyPartyTestEnv(t, "alice")
	env.addProposal(t, "alice", ownPartyID, "completed")
	handler := &HandlerMyParty{Server: &s.Server{DB: env.db}, Config: &config.Config{SatelliteVersion: "2.2"}}
	app := fiber.New()
	app.Patch("/x", handler.PatchMyCompliance)

	res, err := app.Test(httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"website":"https://example.org"}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != 400 {
		t.Fatalf("status=%d, want 400", res.StatusCode)
	}
}

func TestPatchMyComplianceWhenPartyIsMissingFromTheRegistry(t *testing.T) {
	env := newMyPartyTestEnv(t, "alice")
	env.addProposal(t, "alice", "did:ishare:EU.NL.NTRNL-99999999", "completed")

	if status, body := env.patch(t, `{"website":"https://example.org"}`); status != 404 {
		t.Fatalf("status=%d body=%s, want 404", status, body)
	}
}

func TestComplianceClaimID(t *testing.T) {
	cases := []struct {
		name    string
		party   map[string]interface{}
		want    string
		wantErr error
	}{
		{"single", complianceParty("p", claim("a", "frameworkCompliance", "inactive")), "a", nil},
		{
			"active among several",
			complianceParty("p", claim("old", "frameworkCompliance", "revoked"), claim("new", "frameworkCompliance", "active")),
			"new", nil,
		},
		{
			"several active",
			complianceParty("p", claim("a", "frameworkCompliance", "active"), claim("b", "frameworkCompliance", "active")),
			"", errAmbiguousComplianceClaim,
		},
		{"none", complianceParty("p", claim("r", "frameworkRole", "active")), "", errNoComplianceClaim},
		{"v2 party", map[string]interface{}{"party_id": "p"}, "", errNoComplianceClaim},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := complianceClaimID(tc.party)
			if got != tc.want || err != tc.wantErr {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
