package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/send"
)

// A space in a value is written "\ ", the way vim's :set has it.
func TestSetValueWithSpaces(t *testing.T) {
	rc, err := ParseRC(`set workspace=My\ Workspace`)
	if err != nil || rc.Settings.Workspace != "My Workspace" {
		t.Errorf("workspace %q: %v", rc.Settings.Workspace, err)
	}
	// Without the backslash the second word is an option, as in vim, and
	// the error says how to write the space.
	_, err = ParseRC("set workspace=My Workspace")
	if err == nil || !strings.Contains(err.Error(), "Unknown option: Workspace") || !strings.Contains(err.Error(), `workspace=My\ Workspace`) {
		t.Errorf("error: %v", err)
	}
	// :set typed in posthero reads them the same way.
	s := DefaultSettings()
	if _, err := s.Set(`workdir=/tmp/My\ Dir " where files are`); err != nil || s.Workdir != "/tmp/My Dir" {
		t.Errorf("workdir %q: %v", s.Workdir, err)
	}
}

// What :mkpostherorc writes reads back the same.
func TestRCRoundTrip(t *testing.T) {
	rc := RC{Settings: DefaultSettings()}
	rc.Settings.Workdir = "/home/me/My Projects/api"
	rc.Settings.Workspace = `Team "A" \ B`
	rc.Settings.CAFile = `C:\certs\ca.pem`
	rc.Settings.Proxy = "http://proxy:8080"
	rc.Settings.Timeout = 30
	rc.Settings.History = 0
	rc.Settings.Mouse = false
	rc.Maps = []Mapping{{LHS: "gE", RHS: `:env Shop - Local<CR>`}}
	rc.Certs = []send.ClientCert{
		{Host: "api.example.com:443", Cert: "/my certs/c.pem", Key: "/my certs/k.pem"},
		{Host: "b.example.com", PFX: `C:\My Certs\b.pfx`},
	}
	text := RCText(rc)
	back, err := ParseRC(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if back.Settings != rc.Settings {
		t.Errorf("settings:\n%+v\n%+v\n%s", back.Settings, rc.Settings, text)
	}
	if !reflect.DeepEqual(back.Maps, rc.Maps) || !reflect.DeepEqual(back.Certs, rc.Certs) {
		t.Errorf("maps %+v, certs %+v\n%s", back.Maps, back.Certs, text)
	}
}

// postherorc's syntax as vim users write it: tabs, a byte order mark,
// trailing comments.
func TestRCSyntax(t *testing.T) {
	text := "\ufeffset timeout=30 \" seconds\n" +
		"set\tnomouse\n" +
		"  :se  wrap\t\" long lines\n" +
		"\" a comment\n" +
		"# another\n" +
		"set workdir=/tmp/a\\\"b \"comment\n" +
		"set cafile=C:\\certs\\ca.pem\n" +
		"cert host:443 my\\ cert.pem key.pem \" mine\n" +
		"map\tgE :env Shop - Local<CR>\n"
	rc, err := ParseRC(text)
	if err != nil {
		t.Fatal(err)
	}
	s := rc.Settings
	if s.Timeout != 30 || s.Mouse || !s.Wrap || s.Workdir != `/tmp/a"b` || s.CAFile != `C:\certs\ca.pem` {
		t.Errorf("settings: %+v", s)
	}
	if len(rc.Certs) != 1 || rc.Certs[0] != (send.ClientCert{Host: "host:443", Cert: "my cert.pem", Key: "key.pem"}) {
		t.Errorf("certs: %+v", rc.Certs)
	}
	if len(rc.Maps) != 1 || rc.Maps[0] != (Mapping{LHS: "gE", RHS: ":env Shop - Local<CR>"}) {
		t.Errorf("maps: %+v", rc.Maps)
	}
}

// The history option says what 0 does.
func TestHistoryHelp(t *testing.T) {
	for _, row := range (&Settings{}).Table() {
		if row[0] == "history" && !strings.Contains(row[2], "0 keeps none") {
			t.Errorf("history: %s", row[2])
		}
	}
}
