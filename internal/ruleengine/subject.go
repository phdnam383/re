package ruleengine

// Subject kinds, reported by SubjectFacts.Kind.
const (
	// SubjectNone is the pass that runs with no subject bound. It exists so a
	// rule that reasons about named entities rather than about "the thing being
	// examined" still runs exactly once over a snapshot that carries no VDU and
	// no VNFC at all.
	SubjectNone = ""

	SubjectVDU  = "VDU"
	SubjectVNFC = "VNFC"
)

// SubjectFacts is the entity one execution pass is about, bound into GRL under
// the name "Subject".
//
// It carries identity and nothing else. Every question about the subject goes
// through the fact groups under Ctx with Subject.Path() passed in — Ctx.VNFC,
// Ctx.VDU and Ctx.Link already answer them, and giving Subject its own IsDown
// or Parent would create a second way to ask one question and a fact surface
// that doubles every time a fact is added.
//
// Path is empty on the SubjectNone pass. That is deliberately indistinguishable
// from an entity the snapshot does not carry: every fact answers false or zero
// for an unknown path, so a rule written for a subject simply does not match.
type SubjectFacts struct {
	kind string
	path string
}

// Path is the ltree path of the entity this pass is about, or "" when there is
// none.
func (s *SubjectFacts) Path() string { return s.path }

// Kind is SubjectVDU, SubjectVNFC or SubjectNone.
//
// Rules rarely need it: vduByPath and vnfcByPath are separate indexes, so
// Ctx.VNFC.IsDown answers false for a VDU path and Ctx.VDU.IsDegraded answers
// false for a VNFC path. A rule that asks the right fact group has already said
// which kind it means. It is here for the rule that cannot.
func (s *SubjectFacts) Kind() string { return s.kind }

// noSubject is the SubjectNone pass. It is a value rather than a nil pointer
// because GRL calls Subject.Path() unconditionally: a nil binding makes that
// call a reflection panic, which Grule reports as a failed evaluation and which
// would fail the row for every document that mentions Subject at all.
var noSubject = &SubjectFacts{kind: SubjectNone}

// subjects is every entity the rule set will be run against, in a fixed order:
// the no-subject pass, then VDUs, then VNFCs.
//
// The snapshot already sorts both collections by path, so the order is stable
// across runs without sorting again — which matters because the order subjects
// are visited in is the order root causes are asserted in, and that is what the
// response reports.
//
// The list is derived once per analysis and shared by every row. It is a
// property of the snapshot, not of any rule.
func (v *view) subjects() []*SubjectFacts {
	out := make([]*SubjectFacts, 0, 1+len(v.snap.VDUs)+len(v.snap.VNFCs))
	out = append(out, noSubject)
	for _, d := range v.snap.VDUs {
		out = append(out, &SubjectFacts{kind: SubjectVDU, path: d.Path})
	}
	for _, n := range v.snap.VNFCs {
		out = append(out, &SubjectFacts{kind: SubjectVNFC, path: n.Path})
	}
	return out
}
