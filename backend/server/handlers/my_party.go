package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"onboardingportal/config"
	"onboardingportal/models"
	"onboardingportal/responses"
	s "onboardingportal/server"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// HandlerMyParty lets a participant update the self-service fields of their own
// party. The party and claim are resolved server-side from the caller's token;
// the request only carries the new field values.
type HandlerMyParty struct {
	Server *s.Server
	Config *config.Config

	fetchParty func(partyID string) (map[string]interface{}, error)
	patchClaim func(c *fiber.Ctx, partyID, claimID string, body []byte) error
}

func NewHandlerMyParty(server *s.Server, config *config.Config) *HandlerMyParty {
	registry := NewHandlerRegistry(server, config)
	party := NewHandlerParty(server, config)
	return &HandlerMyParty{
		Server:     server,
		Config:     config,
		fetchParty: registry.fetchPartyByExactID,
		patchClaim: func(c *fiber.Ctx, partyID, claimID string, body []byte) error {
			path := config.SatelliteV3Prefix() + "/parties/" + url.PathEscape(partyID) + "/claims/" + url.PathEscape(claimID)
			return party.forwardPartyWriteBody(c, http.MethodPatch, path, body)
		},
	}
}

const (
	proposalStatusCompleted = "completed"
	maxMyComplianceBody     = 16 * 1024
)

var (
	errNoRegisteredParty        = errors.New("no registered party for this account")
	errAmbiguousRegisteredParty = errors.New("this account is linked to more than one registered party")
	errNoComplianceClaim        = errors.New("the party has no frameworkCompliance claim")
	errAmbiguousComplianceClaim = errors.New("the party has more than one active frameworkCompliance claim")
)

// myComplianceUpdate is the complete set of fields a participant may change.
// Anything else in the request body is rejected.
type myComplianceUpdate struct {
	CapabilityURL *string `json:"capabilityUrl"`
	Website       *string `json:"website"`
	CompanyEmail  *string `json:"companyEmail"`
	CompanyPhone  *string `json:"companyPhone"`
	Tags          *string `json:"tags"`
	Description   *string `json:"description"`
}

// PatchMyCompliance godoc
// @Summary      Update the caller's own party details
// @Description  Updates capabilityUrl, website, companyEmail, companyPhone, tags and description on the frameworkCompliance claim of the caller's registered party (v3.0). Omitted fields are left unchanged; any other field is rejected.
// @Tags         registry
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Router       /registry/me/party/compliance [patch]
func (h *HandlerMyParty) PatchMyCompliance(c *fiber.Ctx) error {
	if !strings.HasPrefix(strings.TrimSpace(h.Config.SatelliteVersion), "3") {
		return responses.ErrorResponse(c, fiber.StatusBadRequest, "Editing party details requires a 3.x satellite")
	}
	claims := currentClaims(c)
	if claims == nil || strings.TrimSpace(claims.PreferredUsername) == "" {
		return responses.ErrorResponse(c, fiber.StatusUnauthorized, "Missing authentication claims")
	}
	username := strings.TrimSpace(claims.PreferredUsername)

	patch, err := parseMyComplianceUpdate(c.Body())
	if err != nil {
		return responses.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	partyID, err := registeredPartyIDForUser(h.Server.DB, username)
	if err != nil {
		return myPartyErrorResponse(c, err)
	}

	party, err := h.fetchParty(partyID)
	if err != nil {
		log.Printf("me/party: satellite lookup failed for %q: %v", partyID, err)
		return responses.ErrorResponse(c, fiber.StatusBadGateway, "Failed to load the party from the registry")
	}
	if party == nil {
		return responses.ErrorResponse(c, fiber.StatusNotFound, errNoRegisteredParty.Error())
	}

	claimID, err := complianceClaimID(party)
	if err != nil {
		return myPartyErrorResponse(c, err)
	}

	body, err := json.Marshal(patch)
	if err != nil {
		return responses.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to build the update")
	}
	log.Printf("me/party: user %q updates %v on party %q claim %q", username, sortedKeys(patch), partyID, claimID)
	return h.patchClaim(c, partyID, claimID, body)
}

func myPartyErrorResponse(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, errNoRegisteredParty), errors.Is(err, errNoComplianceClaim):
		return responses.ErrorResponse(c, fiber.StatusNotFound, err.Error())
	case errors.Is(err, errAmbiguousRegisteredParty), errors.Is(err, errAmbiguousComplianceClaim):
		return responses.ErrorResponse(c, fiber.StatusConflict, err.Error())
	default:
		return responses.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
}

// registeredPartyIDForUser returns the party id of the caller's completed
// proposal. Only completed proposals count: before that the party id is applicant
// input (set on propose, changeable on a rejected proposal) and proves nothing.
func registeredPartyIDForUser(db *gorm.DB, username string) (string, error) {
	var proposals []models.Proposal
	if err := db.Where("keycloak_username = ? AND status = ?", username, proposalStatusCompleted).Find(&proposals).Error; err != nil {
		return "", err
	}
	partyIDs := map[string]bool{}
	for _, p := range proposals {
		if id := strings.TrimSpace(p.PartyId); id != "" {
			partyIDs[id] = true
		}
	}
	switch len(partyIDs) {
	case 0:
		return "", errNoRegisteredParty
	case 1:
		for id := range partyIDs {
			return id, nil
		}
	}
	return "", errAmbiguousRegisteredParty
}

// complianceClaimID picks the party's frameworkCompliance claim: the only one, or
// the only active one when the party has several.
func complianceClaimID(party map[string]interface{}) (string, error) {
	var all, active []string
	for _, claim := range partyClaims(party) {
		if t, _ := claim["type"].(string); t != "frameworkCompliance" {
			continue
		}
		id, _ := claim["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		all = append(all, id)
		if status, _ := claim["status"].(string); strings.EqualFold(status, "active") {
			active = append(active, id)
		}
	}
	switch {
	case len(all) == 0:
		return "", errNoComplianceClaim
	case len(all) == 1:
		return all[0], nil
	case len(active) == 1:
		return active[0], nil
	default:
		return "", errAmbiguousComplianceClaim
	}
}

var phonePattern = regexp.MustCompile(`^\+?[0-9 ()./-]{5,32}$`)

// parseMyComplianceUpdate validates the request and maps it to the satellite's
// claim PATCH paths. Empty values clear a field.
func parseMyComplianceUpdate(raw []byte) (map[string]interface{}, error) {
	if len(raw) > maxMyComplianceBody {
		return nil, errors.New("request body too large")
	}
	var update myComplianceUpdate
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update); err != nil {
		return nil, fmt.Errorf("invalid request body: %v", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("invalid request body: unexpected data after the JSON object")
	}

	fields := []struct {
		value    *string
		path     string
		name     string
		maxLen   int
		validate func(string) error
	}{
		{update.CapabilityURL, "capabilityUrl", "capabilityUrl", 2048, validateHTTPURL},
		{update.Website, "additionalInfo.website", "website", 2048, validateHTTPURL},
		{update.CompanyEmail, "additionalInfo.companyEmail", "companyEmail", 254, validateEmail},
		{update.CompanyPhone, "additionalInfo.companyPhone", "companyPhone", 32, validatePhone},
		{update.Tags, "additionalInfo.tags", "tags", 512, nil},
		{update.Description, "additionalInfo.description", "description", 4000, nil},
	}

	patch := map[string]interface{}{}
	for _, f := range fields {
		if f.value == nil {
			continue
		}
		v := strings.TrimSpace(*f.value)
		if len(v) > f.maxLen {
			return nil, fmt.Errorf("%s must be at most %d characters", f.name, f.maxLen)
		}
		if hasDisallowedControlChars(v, f.name == "description") {
			return nil, fmt.Errorf("%s contains invalid characters", f.name)
		}
		if v != "" && f.validate != nil {
			if err := f.validate(v); err != nil {
				return nil, fmt.Errorf("%s %v", f.name, err)
			}
		}
		patch[f.path] = v
	}
	if len(patch) == 0 {
		return nil, errors.New("no fields to update")
	}
	return patch, nil
}

func validateHTTPURL(v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return errors.New("must be an absolute http(s) URL")
	}
	return nil
}

func validateEmail(v string) error {
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Address != v {
		return errors.New("must be a plain e-mail address")
	}
	return nil
}

func validatePhone(v string) error {
	if !phonePattern.MatchString(v) {
		return errors.New("must contain only digits, spaces and + ( ) . / -")
	}
	return nil
}

func hasDisallowedControlChars(v string, allowNewlines bool) bool {
	for _, r := range v {
		if allowNewlines && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
