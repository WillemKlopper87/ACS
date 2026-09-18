package usp

import (
	"fmt"
	"regexp"
)

var firmwareInstancePattern = regexp.MustCompile(`^Device\.FirmwareImage\.([0-9]+)\.$`)

// FirmwareImageInstance validates a discovered TR-181 FirmwareImage object
// path. An explicit instance is required because FirmwareImage is a table;
// the ACS must never guess which slot a CPE should activate.
func FirmwareImageInstance(path string) (string, bool) {
	m := firmwareInstancePattern.FindStringSubmatch(path)
	if len(m) != 2 || m[1] == "0" {
		return "", false
	}
	return "Device.FirmwareImage." + m[1] + ".", true
}

// FirmwareDownloadOperation builds the USP operation identity and arguments
// after the caller has selected an advertised FirmwareImage instance.
func FirmwareDownloadOperation(instance, url string, fileSize int64, autoActivate bool) (string, map[string]string, error) {
	if _, ok := FirmwareImageInstance(instance); !ok {
		return "", nil, fmt.Errorf("invalid USP firmware image instance")
	}
	if url == "" || fileSize < 0 {
		return "", nil, fmt.Errorf("firmware URL and non-negative file size are required")
	}
	return instance + "Download()", map[string]string{
		"URL": url, "FileSize": fmt.Sprintf("%d", fileSize),
		"AutoActivate": fmt.Sprintf("%t", autoActivate),
	}, nil
}
