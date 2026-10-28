package imagecore

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		DriverName:     "driver-qemu",
		ConfigFile:     "driver-qemu-images.json",
		ImageExtension: ".qcow2",
		DownloadSuffix: ".qcow2download",
		SourceURL:      func() string { return "http://localhost/images.json" },
		CacheDir:       func() (string, error) { return "/tmp", nil },
	}
}

// The three drivers' settings must all pass validation.
func TestConfigValidateAcceptsDriverSettings(t *testing.T) {
	cases := map[string]func(c *Config){
		"qemu": func(c *Config) {},
		"vbox": func(c *Config) {
			c.DriverName = "driver-vbox"
			c.ConfigFile = "driver-vbox-images.json"
			c.ImageExtension = ".ova"
			c.DownloadSuffix = ".ovadownload"
		},
		"hyperv": func(c *Config) {
			c.DriverName = "driver-hyperv"
			c.ConfigFile = "driver-hyperv-images.json"
			c.ImageExtension = ".vhdx"
			c.DownloadSuffix = ".download.zip"
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			change(&c)
			if err := c.validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		change func(c *Config)
		field  string
	}{
		{"empty DriverName", func(c *Config) { c.DriverName = "" }, "DriverName"},
		{"DriverName with slash", func(c *Config) { c.DriverName = "a/b" }, "DriverName"},
		{"DriverName with dot", func(c *Config) { c.DriverName = "a.b" }, "DriverName"},
		{"DriverName starts with dash", func(c *Config) { c.DriverName = "-a" }, "DriverName"},
		{"empty ConfigFile", func(c *Config) { c.ConfigFile = "" }, "ConfigFile"},
		{"ConfigFile is dotdot", func(c *Config) { c.ConfigFile = ".." }, "ConfigFile"},
		{"ConfigFile with slash", func(c *Config) { c.ConfigFile = "x/y.json" }, "ConfigFile"},
		{"ConfigFile with backslash", func(c *Config) { c.ConfigFile = `x\y.json` }, "ConfigFile"},
		{"ConfigFile with drive", func(c *Config) { c.ConfigFile = "c:y.json" }, "ConfigFile"},
		{"empty ImageExtension", func(c *Config) { c.ImageExtension = "" }, "ImageExtension"},
		{"ImageExtension without dot", func(c *Config) { c.ImageExtension = "qcow2" }, "ImageExtension"},
		{"ImageExtension only dot", func(c *Config) { c.ImageExtension = "." }, "ImageExtension"},
		{"ImageExtension with slash", func(c *Config) { c.ImageExtension = "./x" }, "ImageExtension"},
		{"empty DownloadSuffix", func(c *Config) { c.DownloadSuffix = "" }, "DownloadSuffix"},
		{"DownloadSuffix with space", func(c *Config) { c.DownloadSuffix = ".a b" }, "DownloadSuffix"},
		{"nil SourceURL", func(c *Config) { c.SourceURL = nil }, "SourceURL"},
		{"nil CacheDir", func(c *Config) { c.CacheDir = nil }, "CacheDir"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.change(&c)
			err := c.validate()
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %q does not mention %s", err, tc.field)
			}
		})
	}
}

func TestConfigValidateReportsEveryProblem(t *testing.T) {
	err := (&Config{}).validate()
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, field := range []string{
		"DriverName", "ConfigFile", "ImageExtension", "DownloadSuffix", "SourceURL", "CacheDir",
	} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not mention %s", err, field)
		}
	}
}

func TestConfigValidateOptionalHooks(t *testing.T) {
	c := validConfig()
	c.Unpack = nil
	c.InUse = nil
	if err := c.validate(); err != nil {
		t.Fatalf("Unpack and InUse are optional: %v", err)
	}
}
