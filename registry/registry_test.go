package registry

import "testing"

func load(t *testing.T) *Repository {
	t.Helper()
	repository, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestTypeOfPath(t *testing.T) {
	repository := load(t)
	tests := []struct{ path, want string }{
		{"force-app/main/default/classes/A.cls", "ApexClass"},
		{"force-app/main/default/classes/A.cls-meta.xml", "ApexClass"},
		{"A\tforce-app/main/default/classes/A.cls", "ApexClass"},
		{"force-app/main/default/objects/Account/fields/F__c.field-meta.xml", "CustomField"},
		{"force-app/main/default/objects/Account/Account.object-meta.xml", "CustomObject"},
		{"force-app/main/default/labels/CustomLabels.labels-meta.xml", "CustomLabel"},
		{"force-app/main/default/lwc/cmp/cmp.js", "LightningComponentBundle"},
		{"force-app/main/default/staticresources/res/icons/a.png", "StaticResource"},
		{"force-app/main/default/reports/Folder/R.report-meta.xml", "Report"},
		{"force-app/main/default/reports/Folder.reportFolder-meta.xml", "Report"},
		{"force-app/main/default/profiles/Admin.profile-meta.xml", "Profile"},
		{"force-app/main/default/objectTranslations/Account-fr/Account-fr.objectTranslation-meta.xml", "CustomObjectTranslation"},
		{"force-app/main/default/objectTranslations/Account-fr/F__c.fieldTranslation-meta.xml", "CustomObjectTranslation"},
		{"force-app/main/default/workflows/Account.workflow-meta.xml", "Workflow"},
		{"README.md", ""},
	}
	for _, test := range tests {
		got := ""
		if metadata := repository.Get(test.path); metadata != nil {
			got = metadata.XMLName
		}
		if got != test.want {
			t.Errorf("Get(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestFullyQualifiedName(t *testing.T) {
	repository := load(t)
	tests := []struct{ path, want string }{
		{"force-app/main/default/classes/A.cls-meta.xml", "classes/A.cls"},
		{"other/classes/A.cls", "classes/A.cls"},
		{"force-app/main/default/objects/Account/fields/F__c.field-meta.xml", "objectsAccountfieldsF__c.field-meta.xml"},
		{"force-app/main/default/lwc/cmp/cmp.js", "lwc/cmp"},
		{"force-app/main/default/lwc/cmp/__tests__/cmp.test.js", "lwc/cmp"},
		{"force-app/main/default/staticresources/res.resource-meta.xml", "staticresources/res"},
		{"force-app/main/default/staticresources/res/icons/a.png", "staticresources/res"},
		{"force-app/main/default/reports/Folder/R.report-meta.xml", "reports/Folder/R"},
		{"force-app/main/default/reports/Folder.reportFolder-meta.xml", "reports/Folder"},
		{"force-app/main/default/permissionsets/PS.permissionset-meta.xml", "permissionsets/PS"},
		{"force-app/main/default/permissionsets/PS/classAccesses/A.classAccess-meta.xml", "permissionsets/PS"},
		{"README.md", "README.md"},
	}
	for _, test := range tests {
		if got := repository.FullyQualifiedName(test.path); got != test.want {
			t.Errorf("FullyQualifiedName(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestPathOfLine(t *testing.T) {
	tests := []struct{ line, want string }{
		{"A\tpath/file", "path/file"},
		{"D  path/file", "path/file"},
		{"path/file", "path/file"},
		{"a b", "b"},
		{"", ""},
	}
	for _, test := range tests {
		if got := PathOfLine(test.line); got != test.want {
			t.Errorf("PathOfLine(%q) = %q, want %q", test.line, got, test.want)
		}
	}
}

func TestDefinitionsKeepRegistryOrder(t *testing.T) {
	definitions, err := Definitions("")
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) < 500 {
		t.Fatalf("only %d definitions", len(definitions))
	}
	last := definitions[len(definitions)-1]
	if last.XMLName != "CustomObjectTranslation" || last.Suffix != "objectTranslation" || !last.PruneOnly {
		t.Errorf("last definition = %+v", last)
	}
	repository := NewRepository(definitions)
	profile := repository.GetByXMLName("Profile")
	if profile == nil || !profile.PruneOnly || profile.XMLTag != "" {
		t.Errorf("Profile = %+v", profile)
	}
	label := repository.GetByXMLName("CustomLabel")
	if label == nil || label.XMLTag != "labels" || label.Key != "fullName" || label.Suffix != "labels" {
		t.Errorf("CustomLabel = %+v", label)
	}
}
