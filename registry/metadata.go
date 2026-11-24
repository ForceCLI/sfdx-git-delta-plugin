// Package registry describes Salesforce metadata types: the directory and
// file suffix of each type, its child types, and how a file path maps to a
// type and a component name.
package registry

// Content is a type that shares the directory of another type and is told
// apart by its file suffix.
type Content struct {
	Suffix  string `json:"suffix,omitempty"`
	XMLName string `json:"xmlName,omitempty"`
}

// Metadata describes one metadata type.
type Metadata struct {
	DirectoryName string    `json:"directoryName"`
	InFolder      bool      `json:"inFolder"`
	MetaFile      bool      `json:"metaFile"`
	Suffix        string    `json:"suffix,omitempty"`
	XMLName       string    `json:"xmlName,omitempty"`
	Content       []Content `json:"content,omitempty"`
	ChildXMLNames []string  `json:"childXmlNames,omitempty"`
	ParentXMLName string    `json:"parentXmlName,omitempty"`
	XMLTag        string    `json:"xmlTag,omitempty"`
	Key           string    `json:"key,omitempty"`
	Excluded      bool      `json:"excluded,omitempty"`
	PruneOnly     bool      `json:"pruneOnly,omitempty"`
	Adapter       string    `json:"adapter,omitempty"`
	Decomposition string    `json:"decomposition,omitempty"`
}

// Names of metadata types, adapters and file parts that the rules of this
// program refer to.
const (
	BotType                        = "Bot"
	BotVersionType                 = "BotVersion"
	CustomApplicationSuffix        = "app"
	CustomMetadataSuffix           = "md"
	DashboardType                  = "Dashboard"
	DefaultContainerDepth          = 1
	DigitalExperienceAdapter       = "digitalExperience"
	DigitalExperienceBundleType    = "DigitalExperienceBundle"
	DigitalExperienceBundleDepth   = 2
	DigitalExperienceContentDepth  = 4
	DigitalExperienceType          = "DigitalExperience"
	EmailServicesFunctionSuffix    = "xml"
	FieldDirectoryName             = "fields"
	FlowDefinitionsKey             = "flowDefinitions"
	FlowXMLName                    = "Flow"
	InFolderSuffix                 = "Folder"
	LabelDecomposedSuffix          = "label"
	MasterDetailTag                = "<type>MasterDetail</type>"
	MetaFileSuffix                 = "-meta.xml"
	ObjectTranslationMetaXMLSuffix = "objectTranslation" + MetaFileSuffix
	ObjectTranslationType          = "CustomObjectTranslation"
	ObjectType                     = "CustomObject"
	PermissionSetType              = "PermissionSet"
	RecordTypeType                 = "RecordType"
	ReportType                     = "Report"
	SharingRuleType                = "SharingRules"
	TranslationExtension           = "translation"
	TranslationType                = "Translations"
	VirtualBotType                 = "VirtualBot"
	WorkflowType                   = "Workflow"
)

// ContentContainerAdapters are the adapters whose component is a directory
// that owns every file below it.
var ContentContainerAdapters = map[string]bool{
	"bundle":                 true,
	DigitalExperienceAdapter: true,
	"mixedContent":           true,
}

// FolderMoveOnDeployTypes are relocated by the Metadata API when a package
// lists them under a new folder, so their former path is not destroyed.
var FolderMoveOnDeployTypes = map[string]bool{ReportType: true, DashboardType: true}

// SubObjectTypes are the types stored in files below an object directory or
// inside a workflow or sharing rules file.
var SubObjectTypes = []string{
	"BusinessProcess",
	"CompactLayout",
	"CustomField",
	"FieldSet",
	"Index",
	"ListView",
	RecordTypeType,
	"SharingCriteriaRule",
	"SharingGuestRule",
	"SharingOwnerRule",
	"SharingReason",
	"Territory2",
	"Territory2Rule",
	"ValidationRule",
	"WebLink",
	"WorkflowAlert",
	"WorkflowFieldUpdate",
	"WorkflowFlowAction",
	"WorkflowKnowledgePublish",
	"WorkflowOutboundMessage",
	"WorkflowRule",
	"WorkflowSend",
	"WorkflowTask",
}

// UndeletableTypes cannot be deleted through the Metadata API.  The
// destructive manifest omits them and a warning names them.
var UndeletableTypes = map[string]bool{RecordTypeType: true}
