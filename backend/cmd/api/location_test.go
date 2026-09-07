package main

import "testing"

func f64(v float64) *float64 { return &v }

// PUT /devices/{id}/location replaces the whole location resource, so a
// request carrying one coordinate but not the other is a client bug, not a
// partial update — half a fix cannot be plotted and silently storing it
// would put a device on the equator or the prime meridian.
func TestValidateCoordinates(t *testing.T) {
	tests := []struct {
		name    string
		lat     *float64
		lon     *float64
		wantErr string
	}{
		{name: "both absent clears the fix"},
		{name: "valid southern hemisphere fix", lat: f64(-25.7479), lon: f64(28.2293)},
		{name: "null island is a real coordinate", lat: f64(0), lon: f64(0)},
		{name: "extremes are inclusive", lat: f64(-90), lon: f64(180)},
		{
			name: "latitude without longitude", lat: f64(-25.7479),
			wantErr: "latitude and longitude must be provided together",
		},
		{
			name: "longitude without latitude", lon: f64(28.2293),
			wantErr: "latitude and longitude must be provided together",
		},
		{
			name: "latitude out of range", lat: f64(91), lon: f64(0),
			wantErr: "latitude must be between -90 and 90",
		},
		{
			name: "longitude out of range", lat: f64(0), lon: f64(-180.5),
			wantErr: "longitude must be between -180 and 180",
		},
		{
			name: "NaN is not a location", lat: f64(nan()), lon: f64(0),
			wantErr: "latitude must be between -90 and 90",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCoordinates(tc.lat, tc.lon)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("expected error %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}
