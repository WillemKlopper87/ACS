package tmf

import "time"

// ServiceOrder is the common TMF641 v4 response shape used by the adapter.
// Optional fields are omitted until ACS has authoritative values for them.
type ServiceOrder struct {
	ID             string             `json:"id"`
	Href           string             `json:"href"`
	ExternalID     string             `json:"externalId"`
	State          string             `json:"state"`
	OrderDate      time.Time          `json:"orderDate"`
	CompletionDate *time.Time         `json:"completionDate,omitempty"`
	RelatedParty   []RelatedParty     `json:"relatedParty"`
	OrderItem      []ServiceOrderItem `json:"orderItem"`
	Note           []Note             `json:"note,omitempty"`
}

type ServiceOrderItem struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	State   string `json:"state"`
	Role    string `json:"role,omitempty"`
	Service any    `json:"service,omitempty"`
}

type RelatedParty struct {
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
}

type Note struct {
	Text string `json:"text"`
}

// ServiceOrderState derives the parent state from child states. Keeping this
// pure makes the lifecycle rule testable and prevents a second stored state
// machine from diverging from the item records.
func ServiceOrderState(cancelled bool, states []string) string {
	if cancelled {
		return "cancelled"
	}
	if len(states) == 0 {
		return "acknowledged"
	}
	completed, failed, outstanding := 0, 0, 0
	for _, state := range states {
		switch state {
		case "COMPLETED", "SKIPPED":
			completed++
		case "FAILED":
			failed++
		default:
			outstanding++
		}
	}
	if outstanding == 0 && failed == 0 {
		return "completed"
	}
	if outstanding == 0 && completed == 0 && failed > 0 {
		return "failed"
	}
	if completed > 0 || failed > 0 {
		return "partial"
	}
	return "acknowledged"
}
