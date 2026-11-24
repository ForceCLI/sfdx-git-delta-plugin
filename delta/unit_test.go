package delta

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSourceDirs(t *testing.T) {
	pathspecs, rejections := parseSourceDirs([]string{"./", "force-app/", "force-app", `win\dir`, ".", "a/./b"})
	if want := []string{".", "force-app", "win/dir", "a/b"}; !reflect.DeepEqual(pathspecs, want) || len(rejections) != 0 {
		t.Errorf("pathspecs = %q, rejections = %q; want %q", pathspecs, rejections, want)
	}
	for value, fragment := range map[string]string{
		"":           "empty value",
		":(exclude)": "pathspec magic",
		"force-*":    "wildcards",
		"a?":         "wildcards",
		"/abs":       "not an absolute path",
		`C:\dir`:     "not an absolute path",
		"a/../b":     "'..' path segments",
	} {
		_, rejections := parseSourceDirs([]string{value})
		if len(rejections) != 1 || !strings.Contains(rejections[0], fragment) {
			t.Errorf("parseSourceDirs(%q) rejections = %q, want one containing %q", value, rejections, fragment)
		}
	}
}

func TestSanitizeForMessage(t *testing.T) {
	if got := sanitizeForMessage("a\\b\x1b[31m\nc"); got != `a\\b\u{1b}[31m\u{a}c` {
		t.Errorf("sanitizeForMessage = %q", got)
	}
	long := strings.Repeat("x", 250)
	if got := sanitizeForMessage(long); len([]rune(got)) != 201 || !strings.HasSuffix(got, "\u2026") {
		t.Errorf("a long value is not cut to 200 characters and an ellipsis: %d runes", len([]rune(got)))
	}
}

func TestToAPIVersion(t *testing.T) {
	for value, want := range map[any]int{"62.0": 62, "62": 62, 61.0: 61, "abc": 0, "-3": 0, "0": 0, nil: 0, true: 0} {
		if got := toAPIVersion(value); got != want {
			t.Errorf("toAPIVersion(%v) = %d, want %d", value, got, want)
		}
	}
}

func element(target ManifestTarget, typeName, member string, kind ChangeKind) ManifestElement {
	return ManifestElement{Target: target, Type: typeName, Member: member, ChangeKind: kind}
}

func members(manifest *Manifest, typeName string) []string {
	return manifest.Get(typeName).Members()
}

func TestDestructiveManifestRules(t *testing.T) {
	changes := NewChangeSet([]ManifestElement{
		element(TargetPackage, "ApexClass", "Both", KindModify),
		element(TargetDestructive, "ApexClass", "Both", KindDelete),
		element(TargetDestructive, "ApexClass", "Gone", KindDelete),
		element(TargetDestructive, "RecordType", "Account.Partner", KindDelete),
		element(TargetDestructive, "RecordType", "Lead.Web", KindDelete),
		element(TargetDestructive, "CustomObject", "Lead", KindDelete),
		element(TargetPackage, "Report", "NewFolder/Sales", KindAdd),
		element(TargetDestructive, "Report", "OldFolder/Sales", KindDelete),
		element(TargetDestructive, "Report", "OldFolder/Other", KindDelete),
		element(TargetDestructive, "Bot", "Helper", KindDelete),
		element(TargetDestructive, "BotVersion", "Helper.v1", KindDelete),
		element(TargetDestructive, "BotVersion", "Kept.v2", KindDelete),
	}, nil)
	destructive := changes.ForDestructiveManifest()
	if got := members(destructive, "ApexClass"); !reflect.DeepEqual(got, []string{"Gone"}) {
		t.Errorf("a component that is also deployed is deleted: %q", got)
	}
	if destructive.Get("RecordType") != nil {
		t.Error("record types are in the destructive manifest")
	}
	if got := members(destructive, "Report"); !reflect.DeepEqual(got, []string{"OldFolder/Other"}) {
		t.Errorf("reports = %q; a report that moved to another folder is not deleted", got)
	}
	if got := members(destructive, "BotVersion"); !reflect.DeepEqual(got, []string{"Kept.v2"}) {
		t.Errorf("bot versions = %q; the versions of a deleted bot are not listed", got)
	}
	undeletable := changes.UndeletableDeletions()
	if got := members(undeletable, "RecordType"); !reflect.DeepEqual(got, []string{"Account.Partner"}) {
		t.Errorf("undeletable = %q; a record type of a deleted object needs no warning", got)
	}
}

func TestRenamesNeedBothSidesInTheRun(t *testing.T) {
	handler := HandlerResult{Elements: []ManifestElement{
		element(TargetPackage, "ApexClass", "After", KindAdd),
		element(TargetDestructive, "ApexClass", "Before", KindDelete),
		element(TargetPackage, "ApexClass", "Lonely", KindAdd),
	}}
	assembled := assembleChanges(handler, HandlerResult{}, []RenameTriple{
		{Type: "ApexClass", From: "Before", To: "After"},
		{Type: "ApexClass", From: "Unknown", To: "Lonely"},
		{Type: "ApexClass", From: "Before", To: "NotPackaged"},
	})
	view := assembled.changes.ByChangeKind()
	if !reflect.DeepEqual(view.Renames, map[string][]RenamePair{"ApexClass": {{From: "Before", To: "After"}}}) {
		t.Errorf("renames = %+v", view.Renames)
	}
	if got := members(view.Add, "ApexClass"); !reflect.DeepEqual(got, []string{"Lonely"}) {
		t.Errorf("added = %q", got)
	}
	if view.Delete.Get("ApexClass") != nil {
		t.Errorf("deleted = %q", members(view.Delete, "ApexClass"))
	}
}

func TestBundleRollup(t *testing.T) {
	kept, warnings := applyBundleRollup([]ManifestElement{
		element(TargetPackage, "DigitalExperienceBundle", "site/Shop", KindModify),
		element(TargetPackage, "DigitalExperience", "site/Shop.sfdc_cms__view/home", KindModify),
		element(TargetPackage, "DigitalExperience", "site/Other.sfdc_cms__view/home", KindModify),
		element(TargetDestructive, "DigitalExperienceBundle", "site/Old", KindDelete),
	})
	if len(kept) != 3 || kept[1].Member != "site/Other.sfdc_cms__view/home" {
		t.Errorf("kept = %+v", kept)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "'site/Old'") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestExpandComponentCopies(t *testing.T) {
	// The helper functions that need a tree are covered by the scenario
	// and end-to-end tests; this checks the prefix rule.
	if got := nextCut("lwc/cmp/cmp.js", 4); got != 7 {
		t.Errorf("nextCut = %d, want 7", got)
	}
	if !isBundleFolder([]string{"cmp.js", "cmp.js-meta.xml"}, "cmp") {
		t.Error("a folder with a meta file named after it is not seen as a bundle")
	}
	if isBundleFolder([]string{"jest.config.js", "other.js-meta.xml"}, "__mocks__") {
		t.Error("a folder without a meta file named after it is seen as a bundle")
	}
}
