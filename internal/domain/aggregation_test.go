package domain

import "testing"

func TestAccountDatasetRegistryIncludesAeroIMNotifications(t *testing.T) {
	const notifications AccountDataset = "aero-im.notifications"
	descriptor, ok := DatasetDescriptorOf(notifications)
	if !ok {
		t.Fatal("aero-im notification projection is missing from the public allow-list")
	}
	if descriptor.Source != "aero-im" || descriptor.PII {
		t.Fatalf("notification descriptor = %+v", descriptor)
	}
}
