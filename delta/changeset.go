package delta

import (
	"strings"

	"github.com/ForceCLI/sfdx-git-delta-plugin/registry"
)

// MemberSet is a set of member names in insertion order.
type MemberSet struct {
	order []string
	has   map[string]bool
}

func newMemberSet(members ...string) *MemberSet {
	set := &MemberSet{has: map[string]bool{}}
	for _, member := range members {
		set.Add(member)
	}
	return set
}

// Add puts a member in the set.
func (s *MemberSet) Add(member string) {
	if !s.has[member] {
		s.has[member] = true
		s.order = append(s.order, member)
	}
}

// Has reports whether the member is in the set.
func (s *MemberSet) Has(member string) bool {
	return s != nil && s.has[member]
}

// Members returns the members in insertion order.
func (s *MemberSet) Members() []string {
	if s == nil {
		return nil
	}
	return s.order
}

// Len returns the number of members.
func (s *MemberSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.order)
}

func (s *MemberSet) filter(keep func(member string) bool) *MemberSet {
	result := newMemberSet()
	for _, member := range s.Members() {
		if keep(member) {
			result.Add(member)
		}
	}
	return result
}

// Manifest maps a metadata type to its members, in insertion order.
type Manifest struct {
	order []string
	sets  map[string]*MemberSet
}

func newManifest() *Manifest {
	return &Manifest{sets: map[string]*MemberSet{}}
}

// Types returns the types in insertion order.
func (m *Manifest) Types() []string {
	return m.order
}

// Get returns the members of a type, or nil.
func (m *Manifest) Get(typeName string) *MemberSet {
	return m.sets[typeName]
}

// Len returns the number of types.
func (m *Manifest) Len() int {
	return len(m.order)
}

func (m *Manifest) add(typeName, member string) {
	set := m.sets[typeName]
	if set == nil {
		set = newMemberSet()
		m.set(typeName, set)
	}
	set.Add(member)
}

func (m *Manifest) set(typeName string, members *MemberSet) {
	if _, exists := m.sets[typeName]; !exists {
		m.order = append(m.order, typeName)
	}
	m.sets[typeName] = members
}

func (m *Manifest) remove(typeName string) {
	if _, exists := m.sets[typeName]; !exists {
		return
	}
	delete(m.sets, typeName)
	kept := m.order[:0:0]
	for _, name := range m.order {
		if name != typeName {
			kept = append(kept, name)
		}
	}
	m.order = kept
}

// shallowCopy returns a manifest with the same member sets.
func (m *Manifest) shallowCopy() *Manifest {
	result := newManifest()
	for _, typeName := range m.order {
		result.set(typeName, m.sets[typeName])
	}
	return result
}

func (m *Manifest) clone() *Manifest {
	result := newManifest()
	for _, typeName := range m.order {
		result.set(typeName, newMemberSet(m.sets[typeName].Members()...))
	}
	return result
}

func unionByType(manifests ...*Manifest) *Manifest {
	result := newManifest()
	for _, manifest := range manifests {
		for _, typeName := range manifest.order {
			for _, member := range manifest.sets[typeName].Members() {
				result.add(typeName, member)
			}
			if result.sets[typeName] == nil {
				result.set(typeName, newMemberSet())
			}
		}
	}
	return result
}

func subtractByType(base, minus *Manifest) *Manifest {
	result := newManifest()
	for _, typeName := range base.order {
		cancellers := minus.Get(typeName)
		remaining := base.sets[typeName].filter(func(member string) bool { return !cancellers.Has(member) })
		if remaining.Len() > 0 {
			result.set(typeName, remaining)
		}
	}
	return result
}

// RenameTriple is a component that was renamed: its type and its member
// name before and after.
type RenameTriple struct {
	Type string
	From string
	To   string
}

// RenamePair is the member name of a renamed component before and after.
type RenamePair struct {
	From string
	To   string
}

type renameBucket struct {
	order []string
	pairs map[string]RenamePair
}

// ChangeSet holds every component change of a run and derives the package
// manifest, the destructive manifest and the changes by kind from them.
//
// The manifest an element goes to and the kind of its change are
// independent: an element can be in the package manifest with the kind
// delete, when a deleted file is treated as an addition to keep the
// elements that still exist.  The manifests follow the target.  The kind
// is used for the changes manifest only.
type ChangeSet struct {
	byTarget    map[ManifestTarget]*Manifest
	byKind      map[ChangeKind]*Manifest
	renameOrder []string
	renames     map[string]*renameBucket
}

// NewChangeSet builds a change set from elements and renames.
func NewChangeSet(elements []ManifestElement, renames []RenameTriple) *ChangeSet {
	set := &ChangeSet{
		byTarget: map[ManifestTarget]*Manifest{TargetPackage: newManifest(), TargetDestructive: newManifest()},
		byKind:   map[ChangeKind]*Manifest{KindAdd: newManifest(), KindModify: newManifest(), KindDelete: newManifest()},
		renames:  map[string]*renameBucket{},
	}
	for _, element := range elements {
		set.byTarget[element.Target].add(element.Type, element.Member)
		set.byKind[element.ChangeKind].add(element.Type, element.Member)
	}
	for _, rename := range renames {
		if rename.From == rename.To {
			continue
		}
		bucket := set.renames[rename.Type]
		if bucket == nil {
			bucket = &renameBucket{pairs: map[string]RenamePair{}}
			set.renames[rename.Type] = bucket
			set.renameOrder = append(set.renameOrder, rename.Type)
		}
		key := rename.From + "\x00" + rename.To
		if _, exists := bucket.pairs[key]; !exists {
			bucket.order = append(bucket.order, key)
		}
		bucket.pairs[key] = RenamePair{From: rename.From, To: rename.To}
	}
	return set
}

// Elements returns one element for each member of each target, with the
// first kind, in the order add, modify, delete, that was recorded for it.
func (c *ChangeSet) Elements() []ManifestElement {
	var result []ManifestElement
	for _, target := range []ManifestTarget{TargetPackage, TargetDestructive} {
		manifest := c.byTarget[target]
		for _, typeName := range manifest.Types() {
			for _, member := range manifest.Get(typeName).Members() {
				for _, kind := range []ChangeKind{KindAdd, KindModify, KindDelete} {
					if c.byKind[kind].Get(typeName).Has(member) {
						result = append(result, ManifestElement{Target: target, Type: typeName, Member: member, ChangeKind: kind})
						break
					}
				}
			}
		}
	}
	return result
}

func (c *ChangeSet) renameSide(pick func(RenamePair) string) *Manifest {
	result := newManifest()
	for _, typeName := range c.renameOrder {
		bucket := c.renames[typeName]
		members := newMemberSet()
		for _, key := range bucket.order {
			members.Add(pick(bucket.pairs[key]))
		}
		result.set(typeName, members)
	}
	return result
}

func (c *ChangeSet) renameTargets() *Manifest {
	return c.renameSide(func(pair RenamePair) string { return pair.To })
}

func (c *ChangeSet) renameSources() *Manifest {
	return c.renameSide(func(pair RenamePair) string { return pair.From })
}

// ForPackageManifest returns the components to deploy.
func (c *ChangeSet) ForPackageManifest() *Manifest {
	return unionByType(c.byTarget[TargetPackage], c.renameTargets())
}

func (c *ChangeSet) orphanedDeletes(packaged *Manifest) *Manifest {
	return subtractByType(unionByType(c.byTarget[TargetDestructive], c.renameSources()), packaged)
}

// developerName is the part of a member name after its folder, in lower
// case.
func developerName(member string) string {
	return strings.ToLower(member[strings.LastIndex(member, "/")+1:])
}

// suppressMovedFolderMembers removes the reports and dashboards that still
// exist under another folder: the Metadata API moves them on deploy.
func suppressMovedFolderMembers(deletes, surviving *Manifest) *Manifest {
	result := deletes.shallowCopy()
	for _, typeName := range []string{registry.ReportType, registry.DashboardType} {
		members := result.Get(typeName)
		kept := surviving.Get(typeName)
		if members == nil || kept == nil {
			continue
		}
		names := map[string]bool{}
		for _, member := range kept.Members() {
			if name := developerName(member); name != "" {
				names[name] = true
			}
		}
		remaining := members.filter(func(member string) bool { return !names[developerName(member)] })
		if remaining.Len() > 0 {
			result.set(typeName, remaining)
		} else {
			result.remove(typeName)
		}
	}
	return result
}

// ForDestructiveManifest returns the components to delete: the deleted
// components that are not deployed, without the types the Metadata API
// cannot delete, the reports and dashboards that moved to another folder,
// and the versions of bots that are deleted themselves.
func (c *ChangeSet) ForDestructiveManifest() *Manifest {
	packaged := c.ForPackageManifest()
	deletes := c.orphanedDeletes(packaged).shallowCopy()
	for typeName := range registry.UndeletableTypes {
		deletes.remove(typeName)
	}
	deletes = suppressMovedFolderMembers(deletes, packaged)
	bots := deletes.Get(registry.BotType)
	versions := deletes.Get(registry.BotVersionType)
	if bots.Len() == 0 || versions.Len() == 0 {
		return deletes
	}
	kept := versions.filter(func(version string) bool { return !bots.Has(strings.Split(version, ".")[0]) })
	result := deletes.shallowCopy()
	if kept.Len() > 0 {
		result.set(registry.BotVersionType, kept)
	} else {
		result.remove(registry.BotVersionType)
	}
	return result
}

// UndeletableDeletions returns the deleted components of the types the
// Metadata API cannot delete, without those whose object is deleted too.
func (c *ChangeSet) UndeletableDeletions() *Manifest {
	orphaned := c.orphanedDeletes(c.ForPackageManifest())
	deletedHolders := orphaned.Get(registry.ObjectType)
	result := newManifest()
	for _, typeName := range []string{registry.RecordTypeType} {
		members := orphaned.Get(typeName)
		if members == nil {
			continue
		}
		orphans := members.filter(func(member string) bool {
			return deletedHolders.Len() == 0 || !deletedHolders.Has(strings.Split(member, ".")[0])
		})
		if orphans.Len() > 0 {
			result.set(typeName, orphans)
		}
	}
	return result
}

// IsEmpty reports whether both manifests are empty.
func (c *ChangeSet) IsEmpty() bool {
	return c.ForPackageManifest().Len() == 0 && c.ForDestructiveManifest().Len() == 0
}

// ByKind is the changes grouped by what happened to the component.
type ByKind struct {
	Add    *Manifest
	Modify *Manifest
	Delete *Manifest
	// RenameTypes and Renames hold the renamed components by type.
	RenameTypes []string
	Renames     map[string][]RenamePair
}

// ByChangeKind returns the changes by kind for the changes manifest.  A
// renamed component is listed as a rename only.
func (c *ChangeSet) ByChangeKind() ByKind {
	targets := c.renameTargets()
	sources := c.renameSources()
	surviving := unionByType(c.byKind[KindAdd], c.byKind[KindModify])
	result := ByKind{
		Add:     subtractByType(c.byKind[KindAdd], targets),
		Modify:  c.byKind[KindModify].clone(),
		Delete:  suppressMovedFolderMembers(subtractByType(c.byKind[KindDelete], unionByType(surviving, sources)), surviving),
		Renames: map[string][]RenamePair{},
	}
	for _, typeName := range c.renameOrder {
		bucket := c.renames[typeName]
		result.RenameTypes = append(result.RenameTypes, typeName)
		for _, key := range bucket.order {
			result.Renames[typeName] = append(result.Renames[typeName], bucket.pairs[key])
		}
	}
	return result
}

// applyBundleRollup removes the DigitalExperience elements whose bundle is
// in the same manifest, and returns a warning for each deleted bundle.
func applyBundleRollup(elements []ManifestElement) ([]ManifestElement, []string) {
	bundles := map[ManifestTarget]*MemberSet{}
	for _, element := range elements {
		if element.Type != registry.DigitalExperienceBundleType {
			continue
		}
		if bundles[element.Target] == nil {
			bundles[element.Target] = newMemberSet()
		}
		bundles[element.Target].Add(element.Member)
	}
	var kept []ManifestElement
	for _, element := range elements {
		if element.Type == registry.DigitalExperienceType {
			if dot := strings.Index(element.Member, "."); dot >= 0 && bundles[element.Target].Has(element.Member[:dot]) {
				continue
			}
		}
		kept = append(kept, element)
	}
	var warnings []string
	for _, member := range bundles[TargetDestructive].Members() {
		warnings = append(warnings, message("warning.DigitalExperienceBundleDeletion", member))
	}
	return kept, warnings
}

type assembly struct {
	changes  *ChangeSet
	copies   []CopyOperation
	warnings []string
}

// assembleChanges merges the results of the handlers and of the
// collectors.  A rename is kept only when the run itself lists its new
// name for deployment and its old name for deployment or deletion.
func assembleChanges(handlerResult, postResult HandlerResult, renames []RenameTriple) assembly {
	combined := mergeResults(handlerResult, postResult)
	kept, rollupWarnings := applyBundleRollup(combined.Elements)
	packaged := newManifest()
	deleted := newManifest()
	for _, element := range kept {
		if element.Target == TargetPackage {
			packaged.add(element.Type, element.Member)
		} else {
			deleted.add(element.Type, element.Member)
		}
	}
	var corroborated []RenameTriple
	for _, rename := range renames {
		if packaged.Get(rename.Type).Has(rename.To) && (packaged.Get(rename.Type).Has(rename.From) || deleted.Get(rename.Type).Has(rename.From)) {
			corroborated = append(corroborated, rename)
		}
	}
	return assembly{
		changes:  NewChangeSet(kept, corroborated),
		copies:   combined.Copies,
		warnings: append(append([]string{}, combined.Warnings...), rollupWarnings...),
	}
}
