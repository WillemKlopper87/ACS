package adapters

import (
	"acs/internal/parameters"
	"sort"
	"strings"
)

type StandardCapabilities struct {
	Cellular              bool            `json:"cellular"`
	SessionManagement     bool            `json:"session_management"`
	PerformanceMonitoring bool            `json:"performance_monitoring"`
	WiFi                  bool            `json:"wifi"`
	MeshDataElements      bool            `json:"mesh_data_elements"`
	BulkData              bool            `json:"bulk_data"`
	TR143                 TR143Capability `json:"tr143"`
}

func ResolveStandardCapabilities(names map[string]bool) StandardCapabilities {
	c := StandardCapabilities{TR143: ResolveTR143Capability(names)}
	for path := range names {
		c.Cellular = c.Cellular || strings.HasPrefix(path, "Device.Cellular.")
		c.SessionManagement = c.SessionManagement || strings.HasPrefix(path, "Device.SessionManagement.")
		c.PerformanceMonitoring = c.PerformanceMonitoring || strings.HasPrefix(path, "Device.PeriodicStatistics.") || strings.HasPrefix(path, "Device.BASAPM.") || strings.HasPrefix(path, "Device.LMAP.")
		c.WiFi = c.WiFi || strings.HasPrefix(path, "Device.WiFi.")
		c.MeshDataElements = c.MeshDataElements || strings.HasPrefix(path, "Device.WiFi.DataElements.Network.")
		c.BulkData = c.BulkData || strings.HasPrefix(path, "Device.BulkData.")
	}
	return c
}

type TopologyNode struct {
	Path   string                            `json:"path"`
	Values map[string]parameters.CachedValue `json:"values"`
}
type WiFiTopology struct {
	Supported bool           `json:"supported"`
	Nodes     []TopologyNode `json:"nodes"`
	Source    string         `json:"source"`
}

// ProjectWiFiTopology preserves the standard Multi-AP / DataElements tree,
// including radio, backhaul and BSS/STA details. Values remain timestamped
// evidence, not inferred live links or an implementation of mesh steering.
func ProjectWiFiTopology(names map[string]bool, cached map[string]parameters.CachedValue) WiFiTopology {
	out := WiFiTopology{Supported: ResolveStandardCapabilities(names).MeshDataElements, Nodes: []TopologyNode{}, Source: "cached Device.WiFi.DataElements.Network; refresh separately"}
	groups := map[string]map[string]parameters.CachedValue{}
	const prefix = "Device.WiFi.DataElements.Network.Device."
	for path, value := range cached {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(path, prefix), ".")
		if len(parts) < 2 || parts[0] == "" || parts[0] == "{i}" {
			continue
		}
		nodePath := prefix + parts[0] + "."
		if groups[nodePath] == nil {
			groups[nodePath] = map[string]parameters.CachedValue{}
		}
		groups[nodePath][path] = value
	}
	for path, values := range groups {
		out.Nodes = append(out.Nodes, TopologyNode{Path: path, Values: values})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Path < out.Nodes[j].Path })
	return out
}
