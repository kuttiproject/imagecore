package imagecore

import (
	"encoding/json"
	"testing"

	"github.com/kuttiproject/drivercore"
)

// oldFormat is an image list as the vbox, hyperv and qemu drivers wrote it
// before imagecore: a JSON object keyed by version, compact, with keys sorted
// by encoding/json, and fields in the order below.
const oldFormat = `{` +
	`"1.28.4":{"ImageK8sVersion":"1.28.4","ImageChecksum":"abc123","ImageSourceURL":"https://example.com/kutti-1.28.4.qcow2","ImageStatus":"Downloaded","ImageDeprecated":true},` +
	`"1.30.1":{"ImageK8sVersion":"1.30.1","ImageChecksum":"def456","ImageSourceURL":"https://example.com/kutti-1.30.1.qcow2","ImageStatus":"Notdownloaded","ImageDeprecated":false}` +
	`}`

func TestImageJSONRoundTripsOldFormat(t *testing.T) {
	list := map[string]*Image{}
	if err := json.Unmarshal([]byte(oldFormat), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(list) != 2 {
		t.Fatalf("got %d images, want 2", len(list))
	}

	old := list["1.28.4"]
	if old.K8sVersion() != "1.28.4" ||
		old.Status() != drivercore.ImageStatusDownloaded ||
		!old.Deprecated() {
		t.Errorf("1.28.4 loaded wrongly: %+v", old)
	}
	if old.checksum != "abc123" || old.sourceURL != "https://example.com/kutti-1.28.4.qcow2" {
		t.Errorf("1.28.4 checksum or URL loaded wrongly: %+v", old)
	}

	cur := list["1.30.1"]
	if cur.K8sVersion() != "1.30.1" ||
		cur.Status() != drivercore.ImageStatusNotDownloaded ||
		cur.Deprecated() {
		t.Errorf("1.30.1 loaded wrongly: %+v", cur)
	}

	out, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != oldFormat {
		t.Errorf("output differs from the old format.\nwant: %s\ngot:  %s", oldFormat, out)
	}
}

func TestImageMarshalFromValues(t *testing.T) {
	img := &Image{
		k8sVersion: "1.29.0",
		checksum:   "c",
		sourceURL:  "u",
		status:     drivercore.ImageStatusUnknown,
		deprecated: true,
	}

	out, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"ImageK8sVersion":"1.29.0","ImageChecksum":"c","ImageSourceURL":"u","ImageStatus":"Unknown","ImageDeprecated":true}`
	if string(out) != want {
		t.Errorf("want %s, got %s", want, out)
	}
}

// Old lists may carry fields this version does not know, and lists written
// by an early version may lack some. Neither is an error.
func TestImageUnmarshalToleratesUnknownAndMissingFields(t *testing.T) {
	img := &Image{}
	err := json.Unmarshal([]byte(`{"ImageK8sVersion":"1.27.0","Extra":42}`), img)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if img.K8sVersion() != "1.27.0" {
		t.Errorf("version: got %q", img.K8sVersion())
	}
	if img.checksum != "" || img.sourceURL != "" || img.status != "" || img.deprecated {
		t.Errorf("missing fields should be empty: %+v", img)
	}
}

func TestImageUnmarshalFailureLeavesImageUnchanged(t *testing.T) {
	store := New(Config{})
	img := &Image{
		store:      store,
		k8sVersion: "1.28.4",
		checksum:   "keep",
		status:     drivercore.ImageStatusDownloaded,
	}

	err := json.Unmarshal([]byte(`{"ImageK8sVersion":"2.0.0","ImageDeprecated":"not a bool"}`), img)
	if err == nil {
		t.Fatal("expected an error")
	}

	if img.k8sVersion != "1.28.4" || img.checksum != "keep" ||
		img.status != drivercore.ImageStatusDownloaded || img.deprecated {
		t.Errorf("image changed by a failed unmarshal: %+v", img)
	}
	if img.store != store {
		t.Error("the store reference must survive unmarshalling")
	}
}

func TestImageUnmarshalKeepsStoreReference(t *testing.T) {
	store := New(Config{})
	img := &Image{store: store}

	if err := json.Unmarshal([]byte(`{"ImageK8sVersion":"1.28.4"}`), img); err != nil {
		t.Fatal(err)
	}
	if img.store != store {
		t.Error("the store reference must survive unmarshalling")
	}
}
