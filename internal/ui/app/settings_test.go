package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shinyvision/posthero/internal/postman"
)

func TestSettingsScriptConflictsAndMetadata(t *testing.T) {
	base := postman.SetScript(nil, "prerequest", "before()")
	base[0].Script.Extra = postman.Extra{"packages": json.RawMessage(`{"team":{}}`)}
	mine := postman.SetScript(base, "prerequest", "mine()")
	remote := postman.SetScript(base, "prerequest", "theirs()")
	if _, err := settingsPatch(ownSettings{events: base}, ownSettings{events: mine}, ownSettings{events: remote}); err == nil || !strings.Contains(err.Error(), "prerequest script changed") {
		t.Fatalf("conflicting script edit: %v", err)
	}
	remote = append([]postman.Event(nil), base...)
	remote[0].Script.ID = "assigned-by-server"
	remote = append(remote, postman.Event{Listen: "test", Disabled: true, Script: postman.Script{ID: "disabled", Exec: postman.SplitLines("disabled()")}})
	p, err := settingsPatch(ownSettings{events: base}, ownSettings{events: mine}, ownSettings{events: remote})
	if err != nil {
		t.Fatal(err)
	}
	got := *p.Event
	if len(got) != 2 || got[0].Script.ID != "assigned-by-server" || !sameJSON(got[0].Script.Extra, base[0].Script.Extra) || !got[1].Disabled || got[1].Script.Exec.Text() != "disabled()" {
		t.Fatal("lost script identity, packages or disabled event")
	}
}
