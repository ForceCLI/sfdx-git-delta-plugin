package ignore

import "testing"

func TestIgnores(t *testing.T) {
	tests := []struct {
		name     string
		patterns string
		path     string
		want     bool
	}{
		{"name at any depth", "*.cls", "force-app/main/default/classes/A.cls", true},
		{"name at any depth, other suffix", "*.cls", "force-app/main/default/classes/A.cls-meta.xml", false},
		{"case is not significant", "*.CLS", "classes/a.cls", true},
		{"directory name at any depth", "classes", "force-app/classes/A.cls", true},
		{"directory only pattern on a directory", "classes/", "force-app/classes/A.cls", true},
		{"directory only pattern on a file", "A.cls/", "force-app/classes/A.cls", false},
		{"anchored by inner slash", "force-app/classes", "force-app/classes/A.cls", true},
		{"anchored pattern below another root", "force-app/classes", "other/force-app/classes/A.cls", false},
		{"leading slash anchors", "/classes", "force-app/classes/A.cls", false},
		{"leading slash at root", "/classes", "classes/A.cls", true},
		{"leading double star", "**/classes/A.cls", "force-app/main/classes/A.cls", true},
		{"leading double star at root", "**/classes/A.cls", "classes/A.cls", true},
		{"middle double star", "force-app/**/A.cls", "force-app/main/default/classes/A.cls", true},
		{"middle double star with no directory", "force-app/**/A.cls", "force-app/A.cls", true},
		{"trailing double star", "force-app/**", "force-app/main/A.cls", true},
		{"trailing double star does not match the directory itself as a file", "force-app/**", "force-app", false},
		{"star does not cross a slash", "force-app/*.cls", "force-app/classes/A.cls", false},
		{"question mark", "A?.cls", "classes/AB.cls", true},
		{"class", "[AB].cls", "classes/B.cls", true},
		{"negated class", "[!AB].cls", "classes/C.cls", true},
		{"negated class excludes", "[!AB].cls", "classes/A.cls", false},
		{"unclosed class matches nothing", "[AB.cls", "classes/[AB.cls", false},
		{"last match wins", "*.cls\n!A.cls", "classes/A.cls", false},
		{"last match wins, ignored again", "*.cls\n!A.cls\nA.cls", "classes/A.cls", true},
		{"file below an ignored directory stays ignored", "classes/\n!classes/A.cls", "classes/A.cls", true},
		{"negation with parent kept", "classes/*\n!classes/A.cls", "classes/A.cls", false},
		{"comment", "#*.cls", "classes/A.cls", false},
		{"escaped hash", `\#a`, "#a", true},
		{"escaped bang", `\!a`, "!a", true},
		{"trailing spaces are dropped", "A.cls   ", "classes/A.cls", true},
		{"escaped trailing space is kept", `A.cls\ `, "classes/A.cls ", true},
		{"windows line endings", "*.cls\r\n*.trigger\r\n", "triggers/T.trigger", true},
		{"dot is literal", "A.cls", "classes/AxCls", false},
		{"empty matcher", "", "classes/A.cls", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Parse(test.patterns).Ignores(test.path); got != test.want {
				t.Errorf("Parse(%q).Ignores(%q) = %v, want %v", test.patterns, test.path, got, test.want)
			}
		})
	}
}

func TestHelperKeep(t *testing.T) {
	helper := &Helper{Global: Parse("*.cls"), Destructive: Parse("*.trigger")}
	tests := []struct {
		line string
		want bool
	}{
		{"A\tclasses/A.cls", false},
		{"M\tclasses/A.cls", false},
		{"D\tclasses/A.cls", true},
		{"D\ttriggers/T.trigger", false},
		{"A\ttriggers/T.trigger", true},
		{"R100\tclasses/A.cls", true},
	}
	for _, test := range tests {
		if got := helper.Keep(test.line); got != test.want {
			t.Errorf("Keep(%q) = %v, want %v", test.line, got, test.want)
		}
	}
	var none *Helper
	if !none.Keep("A\tclasses/A.cls") {
		t.Error("a nil helper ignored a line")
	}
}
