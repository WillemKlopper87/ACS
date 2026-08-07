// Package adapters isolates vendor-specific behavior (design doc v3 §6.3:
// "Vendor differences should be isolated in adapters"). Phase 2 adds the
// first piece of this: a static catalog of each vendor's known parameter
// tree, parsed from hand-curated XML data-model files, and a curated
// subset of cellular/RF diagnostic parameters per vendor for the
// "refresh cellular diagnostics" convenience job. Full canonical-path
// resolution (v3 §6.2, mapping a canonical name to the right vendor path)
// is later-phase work once more of the fleet's actual behavior is
// verified — this just makes the known vendor data available.
package adapters

import (
	"embed"
	"encoding/xml"
	"fmt"
	"strings"
)

//go:embed catalogs/*.xml
var catalogsFS embed.FS

// CatalogParameter is one leaf parameter in a vendor's data model.
type CatalogParameter struct {
	Path   string // full dotted path, e.g. "Device.X_NOKIA_5G_Diagnostics.SS_RSRP"
	Type   string
	Access string // "readOnly" or "readWrite"
}

// Catalog is one vendor/model's known parameter tree.
type Catalog struct {
	Vendor      string
	Model       string
	SpecVersion string
	Parameters  []CatalogParameter
}

type xmlDataModel struct {
	XMLName     xml.Name    `xml:"cwmpDataModel"`
	Vendor      string      `xml:"vendor,attr"`
	Model       string      `xml:"model,attr"`
	SpecVersion string      `xml:"specVersion,attr"`
	Objects     []xmlObject `xml:"object"`
}

type xmlObject struct {
	Name       string         `xml:"name,attr"`
	Access     string         `xml:"access,attr"`
	Objects    []xmlObject    `xml:"object"`
	Parameters []xmlParameter `xml:"parameter"`
}

type xmlParameter struct {
	Name   string `xml:"name,attr"`
	Type   string `xml:"type,attr"`
	Access string `xml:"access,attr"`
}

// parseCatalog parses one vendor XML data-model file and flattens its
// object/parameter tree into full dotted paths. Object names in these
// files already carry their trailing "." (e.g. "DeviceInfo."), so a
// parameter's full path is simply its ancestor object names concatenated
// with its own name — no separator logic needed beyond that convention.
func parseCatalog(raw []byte) (Catalog, error) {
	var dm xmlDataModel
	if err := xml.Unmarshal(raw, &dm); err != nil {
		return Catalog{}, fmt.Errorf("parse catalog xml: %w", err)
	}

	cat := Catalog{Vendor: dm.Vendor, Model: dm.Model, SpecVersion: dm.SpecVersion}
	for _, obj := range dm.Objects {
		flattenObject(obj, "", &cat.Parameters)
	}
	return cat, nil
}

func flattenObject(obj xmlObject, prefix string, out *[]CatalogParameter) {
	path := prefix + obj.Name
	for _, p := range obj.Parameters {
		*out = append(*out, CatalogParameter{
			Path:   path + p.Name,
			Type:   p.Type,
			Access: p.Access,
		})
	}
	for _, child := range obj.Objects {
		flattenObject(child, path, out)
	}
}

// LoadCatalogs parses every embedded vendor XML file. It panics on
// failure — these are build-time-fixed reference files shipped with the
// binary, not user input, so a malformed one is a build error, not a
// runtime condition callers should have to handle.
func LoadCatalogs() map[string]Catalog {
	entries, err := catalogsFS.ReadDir("catalogs")
	if err != nil {
		panic(fmt.Sprintf("adapters: read embedded catalogs: %v", err))
	}

	catalogs := make(map[string]Catalog, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		raw, err := catalogsFS.ReadFile("catalogs/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("adapters: read catalog %s: %v", e.Name(), err))
		}
		cat, err := parseCatalog(raw)
		if err != nil {
			panic(fmt.Sprintf("adapters: %s: %v", e.Name(), err))
		}
		catalogs[strings.ToLower(cat.Vendor)] = cat
	}
	return catalogs
}
