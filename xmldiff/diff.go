package xmldiff

import (
	"fmt"
	"io"
	"sort"
)

// Attribute describes a child element of an in-file type: the metadata type
// it stands for and the child element that holds its name.
type Attribute struct {
	XMLName string
	// Key is the tag of the child that names the element, ArrayKey,
	// ObjectKey, or "" for an element that has no name.
	Key      string
	Excluded bool
}

// Attributes maps the tag of a child of the root to its description.
type Attributes map[string]Attribute

const (
	// ArrayKey marks a tag whose elements are compared as one list.
	ArrayKey = "<array>"
	// ObjectKey marks a tag whose elements are compared one by one by
	// their whole content.
	ObjectKey = "<object>"
	// CardinalitySafetyLimit is the largest number of elements of one tag
	// the engine holds.
	CardinalitySafetyLimit = 100_000
)

// CompareEntry names a component: its metadata type and member name.
type CompareEntry struct {
	Type   string
	Member string
}

// Outcome is the result of comparing two revisions of a file.
type Outcome struct {
	Added    []CompareEntry
	Modified []CompareEntry
	Deleted  []CompareEntry
	// HasPackageContent is true when the later revision has an element
	// that was added or changed, of any kind.  A file from which elements
	// were only deleted leaves it false.
	HasPackageContent bool
	// Writer writes the later revision reduced to the elements that were
	// added or changed.  It is nil when nothing is to be written.
	Writer func(out io.Writer) error
}

type bucket struct {
	order []string
	items map[string][]any
}

func newBucket() *bucket {
	return &bucket{items: map[string][]any{}}
}

func (b *bucket) appendTo(subType string, element any) int {
	if _, seen := b.items[subType]; !seen {
		b.order = append(b.order, subType)
	}
	b.items[subType] = append(b.items[subType], element)
	return len(b.items[subType])
}

type keyedElement struct {
	member  string
	element any
}

type keyedMap struct {
	order  []string
	values map[string]keyedElement
}

type fingerprinted struct {
	element     any
	fingerprint string
}

// Engine compares the children of the root of two revisions.  The earlier
// revision is passed to OnFromElement first, then the later one to
// OnToElement, then Finalize is called.
type Engine struct {
	attributes    Attributes
	generateDelta bool

	fromKeyedOrder []string
	fromKeyed      map[string]*keyedMap
	fromArrays     *bucket
	fromObjects    map[string]map[string]bool
	fromKeyless    *bucket
	fromUnknown    *bucket

	toArrays      *bucket
	toObjectOrder []string
	toObjects     map[string][]fingerprinted
	toKeyless     *bucket
	toUnknown     *bucket

	pruned         map[string][]any
	toSubTypeOrder []string
	seenSubTypes   map[string]bool
	added          []CompareEntry
	modified       []CompareEntry
	deleted        []CompareEntry
	hasSurviving   bool
	identities     int
	err            error
}

// NewEngine returns an engine.  With generateDelta it keeps the elements of
// the later revision that a writer needs.
func NewEngine(attributes Attributes, generateDelta bool) *Engine {
	return &Engine{
		attributes:    attributes,
		generateDelta: generateDelta,
		fromKeyed:     map[string]*keyedMap{},
		fromArrays:    newBucket(),
		fromObjects:   map[string]map[string]bool{},
		fromKeyless:   newBucket(),
		fromUnknown:   newBucket(),
		toArrays:      newBucket(),
		toObjects:     map[string][]fingerprinted{},
		toKeyless:     newBucket(),
		toUnknown:     newBucket(),
		pruned:        map[string][]any{},
		seenSubTypes:  map[string]bool{},
	}
}

func (e *Engine) guard(size int, subType string) {
	if size > CardinalitySafetyLimit && e.err == nil {
		e.err = fmt.Errorf("cardinality safety limit exceeded for %s", subType)
	}
}

// keyOf returns the value of the naming child of an element as a map key
// and as a member name.  A name that is not plain text never equals the
// name of another element.
func (e *Engine) keyOf(element any, keyField string) (key, member string, ok bool) {
	object, isObject := element.(*Object)
	if !isObject {
		return "", "", false
	}
	value, present := object.Get(keyField)
	if !present {
		return "", "", false
	}
	if text, isText := value.(string); isText {
		return "s:" + text, text, true
	}
	e.identities++
	return fmt.Sprintf("i:%d", e.identities), jsString(value), true
}

// OnFromElement records an element of the earlier revision.
func (e *Engine) OnFromElement(subType string, element any) {
	attribute, known := e.attributes[subType]
	switch {
	case !known:
		e.guard(e.fromUnknown.appendTo(subType, element), subType)
	case attribute.Key == "":
		e.guard(e.fromKeyless.appendTo(subType, element), subType)
	case attribute.Key == ArrayKey:
		e.guard(e.fromArrays.appendTo(subType, element), subType)
	case attribute.Key == ObjectKey:
		set := e.fromObjects[subType]
		if set == nil {
			set = map[string]bool{}
			e.fromObjects[subType] = set
		}
		set[Fingerprint(element)] = true
		e.guard(len(set), subType)
	default:
		key, member, ok := e.keyOf(element, attribute.Key)
		if !ok {
			return
		}
		keyed := e.fromKeyed[subType]
		if keyed == nil {
			keyed = &keyedMap{values: map[string]keyedElement{}}
			e.fromKeyed[subType] = keyed
			e.fromKeyedOrder = append(e.fromKeyedOrder, subType)
		}
		if _, exists := keyed.values[key]; !exists {
			keyed.order = append(keyed.order, key)
		}
		keyed.values[key] = keyedElement{member: member, element: element}
		e.guard(len(keyed.values), subType)
	}
}

// OnToElement classifies an element of the later revision.
func (e *Engine) OnToElement(subType string, element any) {
	if !e.seenSubTypes[subType] {
		e.seenSubTypes[subType] = true
		e.toSubTypeOrder = append(e.toSubTypeOrder, subType)
	}
	attribute, known := e.attributes[subType]
	switch {
	case !known:
		e.guard(e.toUnknown.appendTo(subType, element), subType)
	case attribute.Key == "":
		e.guard(e.toKeyless.appendTo(subType, element), subType)
	case attribute.Key == ArrayKey:
		e.guard(e.toArrays.appendTo(subType, element), subType)
	case attribute.Key == ObjectKey:
		if _, seen := e.toObjects[subType]; !seen {
			e.toObjectOrder = append(e.toObjectOrder, subType)
		}
		e.toObjects[subType] = append(e.toObjects[subType], fingerprinted{element: element, fingerprint: Fingerprint(element)})
		e.guard(len(e.toObjects[subType]), subType)
	default:
		key, member, ok := e.keyOf(element, attribute.Key)
		if !ok {
			return
		}
		keyed := e.fromKeyed[subType]
		var previous keyedElement
		found := false
		if keyed != nil {
			previous, found = keyed.values[key]
		}
		if !found {
			if !attribute.Excluded {
				e.added = append(e.added, CompareEntry{Type: attribute.XMLName, Member: member})
			}
			e.retain(subType, element)
			return
		}
		delete(keyed.values, key)
		if DeepEqual(previous.element, element) {
			return
		}
		if !attribute.Excluded {
			e.modified = append(e.modified, CompareEntry{Type: attribute.XMLName, Member: member})
		}
		e.retain(subType, element)
	}
}

func (e *Engine) retain(subType string, element any) {
	e.hasSurviving = true
	if e.generateDelta {
		e.pruned[subType] = append(e.pruned[subType], element)
	}
}

func asList(elements []any) any {
	if elements == nil {
		return []any{}
	}
	return elements
}

// Finalize compares the tags that are compared as a whole and lists the
// named elements that only the earlier revision has.
func (e *Engine) Finalize() (*Outcome, error) {
	if e.err != nil {
		return nil, e.err
	}
	for _, subType := range e.toArrays.order {
		to := e.toArrays.items[subType]
		if !DeepEqual(asList(e.fromArrays.items[subType]), asList(to)) {
			e.hasSurviving = true
			if e.generateDelta {
				e.pruned[subType] = to
			}
		}
	}
	for _, subType := range e.toObjectOrder {
		from := e.fromObjects[subType]
		var retained []any
		for _, item := range e.toObjects[subType] {
			if !from[item.fingerprint] {
				retained = append(retained, item.element)
			}
		}
		if len(retained) > 0 {
			e.hasSurviving = true
			if e.generateDelta {
				e.pruned[subType] = retained
			}
		}
	}
	// A tag without names, or one the registry does not know, is written
	// whole also when it did not change: the file is not deployable
	// without the tags that identify its container.
	for _, pair := range [][2]*bucket{{e.toKeyless, e.fromKeyless}, {e.toUnknown, e.fromUnknown}} {
		to, from := pair[0], pair[1]
		for _, subType := range to.order {
			if !DeepEqual(asList(from.items[subType]), asList(to.items[subType])) {
				e.hasSurviving = true
			}
			if e.generateDelta {
				e.pruned[subType] = to.items[subType]
			}
		}
	}
	for _, subType := range e.fromKeyedOrder {
		attribute := e.attributes[subType]
		if attribute.Excluded {
			continue
		}
		keyed := e.fromKeyed[subType]
		for _, key := range keyed.order {
			if remaining, ok := keyed.values[key]; ok {
				e.deleted = append(e.deleted, CompareEntry{Type: attribute.XMLName, Member: remaining.member})
			}
		}
	}
	return &Outcome{Added: e.added, Modified: e.modified, Deleted: e.deleted, HasPackageContent: e.hasSurviving}, nil
}

// writer returns the function that writes the retained elements under the
// root of the later revision, in the order their tags first occur there.
func (e *Engine) writer(capture *RootCapture) func(io.Writer) error {
	if !e.generateDelta || !e.hasSurviving || capture == nil {
		return nil
	}
	var children []Entry
	for _, subType := range e.toSubTypeOrder {
		if elements, ok := e.pruned[subType]; ok {
			children = append(children, Entry{Key: subType, Value: elements})
		}
	}
	return func(out io.Writer) error {
		return WriteDocument(out, capture, children, WriteOptions{})
	}
}

func outcomeOf(engine *Engine, capture *RootCapture) (*Outcome, error) {
	outcome, err := engine.Finalize()
	if err != nil {
		return nil, err
	}
	outcome.Writer = engine.writer(capture)
	return outcome, nil
}

// Compare compares two revisions of a document given as text.  An empty
// later revision lists every named element of the earlier one as deleted.
// A later revision that is malformed is an error.
func Compare(attributes Attributes, generateDelta bool, fromSource, toSource string) (*Outcome, error) {
	engine := NewEngine(attributes, generateDelta)
	ParseFromSide(fromSource, engine.OnFromElement)
	var capture *RootCapture
	if toSource != "" {
		var err error
		if capture, err = ParseToSide(toSource, engine.OnToElement); err != nil {
			return nil, err
		}
	}
	return outcomeOf(engine, capture)
}

// Reader returns the content of a file at a revision.
type Reader interface {
	StringContent(path, revision string) (string, error)
}

// Differ compares files between two revisions.
type Differ struct {
	From          string
	To            string
	GenerateDelta bool
	Attributes    Attributes
	Reader        Reader
}

// Run compares the two revisions of the file at path.  A revision that
// cannot be read counts as empty.
func (d *Differ) Run(path string) (*Outcome, error) {
	toSource, err := d.Reader.StringContent(path, d.To)
	if err != nil {
		toSource = ""
	}
	fromSource, err := d.Reader.StringContent(path, d.From)
	if err != nil {
		fromSource = ""
	}
	return Compare(d.Attributes, d.GenerateDelta, fromSource, toSource)
}

// UnionInput names the files of one container, such as the labels files of
// one package directory, that are compared as if they were one file.
type UnionInput struct {
	AdditionBasePaths []string
	DeletionBasePaths []string
	ShieldPaths       []string
	MemberPaths       []string
}

type recordedElement struct {
	subType string
	element any
}

func entryKey(entry CompareEntry) string {
	return entry.Type + "\t" + entry.Member
}

// RunUnion compares each shield file at the later revision with the
// elements of all the addition base files at the earlier revision.  An
// element is deleted only when every shield file lacks it and it is in a
// deletion base file.  The result has an outcome for each member path.
func (d *Differ) RunUnion(input UnionInput) (map[string]*Outcome, error) {
	recorded := map[string][]recordedElement{}
	basePaths := append([]string{}, input.AdditionBasePaths...)
	for _, path := range basePaths {
		source, err := d.Reader.StringContent(path, d.From)
		if err != nil {
			return nil, err
		}
		var elements []recordedElement
		ParseFromSide(source, func(subType string, element any) {
			elements = append(elements, recordedElement{subType: subType, element: element})
		})
		recorded[path] = elements
	}
	// The order in which the base files are replayed decides which of two
	// elements with the same name is kept, so it is the sorted path order.
	sortedBase := make([]string, 0, len(recorded))
	for path := range recorded {
		sortedBase = append(sortedBase, path)
	}
	sort.Strings(sortedBase)
	replay := func(engine *Engine, elements []recordedElement) {
		for _, item := range elements {
			engine.OnFromElement(item.subType, item.element)
		}
	}

	deletionBase := map[string]bool{}
	for _, path := range input.DeletionBasePaths {
		deletionBase[path] = true
	}
	deletionEngine := NewEngine(d.Attributes, false)
	for _, path := range sortedBase {
		if deletionBase[path] {
			replay(deletionEngine, recorded[path])
		}
	}
	deletionOutcome, err := deletionEngine.Finalize()
	if err != nil {
		return nil, err
	}
	deletable := map[string]bool{}
	for _, entry := range deletionOutcome.Deleted {
		deletable[entryKey(entry)] = true
	}

	members := map[string]bool{}
	for _, path := range input.MemberPaths {
		members[path] = true
	}
	var shieldOrder []string
	outcomes := map[string]*Outcome{}
	for _, path := range input.ShieldPaths {
		engine := NewEngine(d.Attributes, members[path] && d.GenerateDelta)
		for _, base := range sortedBase {
			replay(engine, recorded[base])
		}
		toSource, err := d.Reader.StringContent(path, d.To)
		if err != nil {
			return nil, err
		}
		var capture *RootCapture
		if toSource != "" {
			if capture, err = ParseToSide(toSource, engine.OnToElement); err != nil {
				return nil, err
			}
		}
		outcome, err := outcomeOf(engine, capture)
		if err != nil {
			return nil, err
		}
		if _, seen := outcomes[path]; !seen {
			shieldOrder = append(shieldOrder, path)
		}
		outcomes[path] = outcome
	}

	var deleted []CompareEntry
	if len(shieldOrder) > 0 {
		for _, entry := range outcomes[shieldOrder[0]].Deleted {
			key := entryKey(entry)
			inAll := deletable[key]
			for _, path := range shieldOrder[1:] {
				if !inAll {
					break
				}
				inAll = false
				for _, other := range outcomes[path].Deleted {
					if entryKey(other) == key {
						inAll = true
						break
					}
				}
			}
			if inAll {
				deleted = append(deleted, entry)
			}
		}
	}
	result := map[string]*Outcome{}
	for _, path := range shieldOrder {
		if !members[path] {
			continue
		}
		outcome := *outcomes[path]
		outcome.Deleted = deleted
		result[path] = &outcome
	}
	return result, nil
}
