package usp

import "testing"

func TestFirmwareImageInstance(t *testing.T) {
	if got, ok := FirmwareImageInstance("Device.FirmwareImage.2."); !ok || got != "Device.FirmwareImage.2." {
		t.Fatalf("instance = %q, ok=%v", got, ok)
	}
	for _, path := range []string{"Device.FirmwareImage.", "Device.FirmwareImage.0.", "Device.X_VENDOR.FirmwareImage.1."} {
		if _, ok := FirmwareImageInstance(path); ok {
			t.Errorf("accepted invalid instance %q", path)
		}
	}
}

func TestFirmwareDownloadOperation(t *testing.T) {
	command, args, err := FirmwareDownloadOperation("Device.FirmwareImage.1.", "https://fw.example/fw.bin", 42, false)
	if err != nil || command != "Device.FirmwareImage.1.Download()" || args["FileSize"] != "42" || args["AutoActivate"] != "false" {
		t.Fatalf("command=%q args=%v err=%v", command, args, err)
	}
}
