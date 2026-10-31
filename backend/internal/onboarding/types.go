package onboarding

import (
	"strings"
	"time"
)

// Site is where CPEs are installed. ExternalRef mirrors a BSS premise id.
type Site struct {
	ID          string
	CustomerID  *string
	ExternalRef *string
	Name        string
	Street      string
	Suburb      string
	City        string
	PostalCode  string
	Country     string
	Latitude    *float64
	Longitude   *float64
	RegionID    *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CellularProfile is what a plan sets on the CPE's mobile connection. Only
// fields present in the device's discovered, writable data model are
// written, and only when they differ from the cached value: a wrong APN
// cuts the CPE's data path, and with it the ACS.
type CellularProfile struct {
	APN          string `json:"apn,omitempty"`
	DNN          string `json:"dnn,omitempty"`
	MTU          int    `json:"mtu,omitempty"`
	BootstrapAPN string `json:"bootstrap_apn,omitempty"`
}

func (c CellularProfile) empty() bool {
	return c.APN == "" && c.DNN == "" && c.MTU == 0 && c.BootstrapAPN == ""
}

// ServicePlan is what a sale means for the CPE (the BSS servicePlan code).
type ServicePlan struct {
	ID               string
	CustomerID       *string
	Code             string
	Name             string
	TemplateIDs      []string
	Cellular         CellularProfile
	AllowedPLMNs     []string
	RequireDiscovery bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// HasConfiguration reports whether the plan writes anything to the CPE.
func (p *ServicePlan) HasConfiguration() bool {
	return p != nil && (len(p.TemplateIDs) > 0 || !p.Cellular.empty())
}

// AllowsIMSI reports whether a SIM's home network (the IMSI's MCC+MNC
// prefix, 5 or 6 digits) is one the plan allows. No list allows any.
func (p *ServicePlan) AllowsIMSI(imsi string) bool {
	if p == nil || len(p.AllowedPLMNs) == 0 {
		return true
	}
	for _, plmn := range p.AllowedPLMNs {
		if plmn = strings.TrimSpace(plmn); plmn != "" && strings.HasPrefix(imsi, plmn) {
			return true
		}
	}
	return false
}

// Onboarding is one CPE's way from sale to service.
type Onboarding struct {
	ID              string
	CustomerID      *string
	DeviceID        *string
	ExpectedOUI     string
	ExpectedSerial  string
	ExpectedIMEI    string
	ExpectedICCID   string
	ExpectedIMSI    string
	AccountRef      string
	SiteID          *string
	ServicePlanID   *string
	Role            string
	CommercialModel string
	Source          string
	ExternalRef     string
	State           string
	StateReason     string
	StateCode       string
	ConfigJobs      []string
	RequestedBy     string
	// BSSRole and BSSServicePlan are what a BSS order asked for, kept for
	// the account mapping cmd/bssadapter writes on first contact.
	BSSRole        string
	BSSServicePlan string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ContactedAt    *time.Time
	InServiceAt    *time.Time
}

// Open reports whether the onboarding can still progress or be retried.
func (o Onboarding) Open() bool {
	return o.State != StateInService && o.State != StateCancelled
}
