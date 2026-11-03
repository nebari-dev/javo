// Package report builds the admin capability report from ClusterStorage. The
// admin API and `javo storage report` both render its rows, so they always say
// the same thing. Rows needing attention come first.
package report

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Row is one fact about the cluster's storage.
type Row struct {
	// Fact names the row: home, shared, folders, snapshots, size, probe,
	// nodes, mismatch, move, ready, or note:<code>.
	Fact string `json:"fact"`
	// Provenance says where the fact comes from: known, override, verified or
	// unknown for a StorageClass fact; auto or override for the mode choice.
	Provenance string `json:"provenance,omitempty"`
	// Attention is true for a row that wants an admin to act.
	Attention bool           `json:"attention"`
	Notice    reasons.Notice `json:"notice"`
	// Class is the StorageClass a shared-storage or note row is about.
	Class string `json:"class,omitempty"`
	// Evidence is what was found, one line each.
	Evidence []string `json:"evidence"`
}

// Build returns the rows for cs and the homes, those needing attention first
// (errors, then warnings), otherwise in a fixed order. The homes are read only
// for the ones that failed to mount.
func Build(cs *model.ClusterStorage, homes []model.Home) []Row {
	st := &cs.Status
	if st.Choice == nil {
		return []Row{notice("home", "", reasons.AdminNotDetected)}
	}
	ch := st.Choice
	class := func(name string) *model.ClassCapability {
		if i := slices.IndexFunc(st.Classes, func(c model.ClassCapability) bool { return c.Name == name }); i >= 0 {
			return &st.Classes[i]
		}
		return nil
	}
	home, shared := class(ch.HomeClass), class(ch.SharedClass)
	single := SingleNode(cs)
	var rows []Row

	homeCode := reasons.AdminHomePerUser
	if ch.Mode == model.HomeModeShared {
		homeCode = reasons.AdminHomeShared
	}
	homeRow := notice("home", string(ch.Source), homeCode, ch.HomeClass)
	if home != nil && home.VolumesPerNode != nil && ch.Mode == model.HomeModePerUser {
		homeRow.Evidence = append(homeRow.Evidence, reasons.Msg(reasons.MsgHomesPerNode, *home.VolumesPerNode, home.Provisioner))
	}
	rows = append(rows, homeRow)
	if c := meta.FindStatusCondition(st.Conditions, model.ConditionHomeStorageReady); c != nil && c.Status == "False" {
		rows = append(rows, fromReason("ready", c.Reason, c.Message))
	}

	switch {
	case ch.Mode == model.HomeModeShared:
	case shared != nil:
		code := reasons.AdminSharedFoundNotVerified
		if shared.RWXFile.Provenance == model.ProvenanceVerified {
			code = reasons.AdminSharedFoundTested
		}
		r := notice("shared", string(shared.RWXFile.Provenance), code, shared.Name, shared.Provisioner)
		r.Class = shared.Name
		// Unused shared storage would let apps spread over several machines.
		r.Attention = !single
		rows = append(rows, r)
	default:
		r := notice("shared", string(model.ProvenanceUnknown), reasons.AdminNoSharedStorage)
		// With one machine, apps run together anyway: nobody is limited.
		r.Attention = r.Attention && !single
		rows = append(rows, r)
	}
	if shared != nil {
		rows = append(rows, notice("folders", string(shared.RWXFile.Provenance), reasons.AdminFoldersOn, shared.Name))
	} else {
		rows = append(rows, notice("folders", string(model.ProvenanceUnknown), reasons.AdminFoldersOff))
	}

	if home != nil {
		snaps := reasons.AdminNoSnapshots
		if home.Snapshots.Value {
			snaps = reasons.AdminSnapshotsOn
		}
		rows = append(rows, notice("snapshots", string(home.Snapshots.Provenance), snaps, home.Name))
		size, prov := reasons.AdminSizeFixed, home.Expansion.Provenance
		switch {
		case !home.SizeEnforced.Value:
			size, prov = reasons.AdminSizeNotEnforced, home.SizeEnforced.Provenance
		case home.Expansion.Value:
			size = reasons.AdminSizeGrows
		}
		rows = append(rows, notice("size", string(prov), size, home.Name))
	}
	if r, ok := mountRow(cs, homes); ok {
		rows = append(rows, r)
	}
	for _, r := range noteRows(home, shared, ch.Mode) {
		r.Attention = r.Attention && !single
		rows = append(rows, r)
	}

	for _, p := range st.ProbeResults {
		rows = append(rows, probeRow(p))
	}
	if st.Probe != nil && st.Probe.Result != model.ProbeRunning && !slices.ContainsFunc(st.ProbeResults, func(p model.ProbeStatus) bool { return p.Nonce == st.Probe.Nonce }) {
		rows = append(rows, probeRow(*st.Probe))
	}

	if c := meta.FindStatusCondition(st.Conditions, model.ConditionSingleNodeAssumption); c != nil && c.Status == "True" {
		r := notice("nodes", "", reasons.AdminSecondNodeJoined)
		r.Evidence = lines(c.Message)
		rows = append(rows, r)
	}
	if m := st.Migration; m != nil {
		if c := meta.FindStatusCondition(st.Conditions, model.ConditionHomeModeMismatch); c != nil && c.Status == "True" {
			r := notice("mismatch", "", reasons.AdminModeMismatch, m.Users-m.Done, reasons.ModeWords(m.To == model.HomeModeShared))
			r.Evidence = lines(c.Message)
			if m.Failed > 0 {
				r.Attention = true
				r.Evidence = append(r.Evidence, fmt.Sprintf("%d homes failed to move", m.Failed))
			}
			rows = append(rows, r)
		}
	}
	if at := cs.Spec.Migration.StartAt; at != nil && !at.IsZero() {
		rows = append(rows, notice("move", "", reasons.AdminMoveScheduled,
			reasons.ModeWords(cs.Spec.Migration.To == model.HomeModeShared), reasons.FormatWhen(at.Time)))
	}

	// Errors, then warnings, then facts; each group keeps its order.
	slices.SortStableFunc(rows, func(a, b Row) int { return cmp.Compare(rank(a), rank(b)) })
	return rows
}

// SingleNode is true while the cluster has one machine, or before the
// controller has looked: every app runs next to its owner's files anyway.
func SingleNode(cs *model.ClusterStorage) bool {
	ch := cs.Status.Choice
	return ch == nil || ch.SingleNode || cs.Status.Nodes <= 1
}

// Summary is the first line of the admin page: where files are, and that
// nothing needs attention when no row asks for it. It is empty before the
// controller has looked at the cluster; the one row then says so.
func Summary(cs *model.ClusterStorage, rows []Row) string {
	ch := cs.Status.Choice
	if ch == nil {
		return ""
	}
	code := reasons.MsgStorageSummaryPerUser
	switch {
	case ch.Mode == model.HomeModeShared:
		code = reasons.MsgStorageSummaryShared
	case SingleNode(cs):
		code = reasons.MsgStorageSummaryOneMachine
	}
	s := reasons.Msg(code)
	if !slices.ContainsFunc(rows, func(r Row) bool { return r.Attention }) {
		s += " " + reasons.Msg(reasons.MsgStorageNothingNeeded)
	}
	return s
}

func rank(r Row) int {
	switch {
	case r.Attention && r.Notice.Tone == reasons.ToneErr:
		return 0
	case r.Attention:
		return 1
	}
	return 2
}

func notice(fact, prov string, code reasons.NoticeCode, args ...any) Row {
	n := reasons.NoticeFor(code, args...)
	return Row{Fact: fact, Provenance: prov, Attention: n.Tone != reasons.ToneInfo, Notice: n, Evidence: []string{}}
}

// fromReason makes a row from a catalog Reason that a status condition names;
// the condition's message is the evidence.
func fromReason(fact, code, message string) Row {
	r, _ := reasons.Lookup(reasons.Code(code))
	return Row{Fact: fact, Attention: true, Evidence: lines(message), Notice: reasons.Notice{
		Code: reasons.NoticeCode(code), Tone: reasons.ToneErr, Title: r.Title, Text: r.What, Action: r.Todo,
	}}
}

// noteRows are the notes of the home class and, when it is another class, of
// the shared class. NeedsNfsClient is about shared files, so it is shown only
// for the class that would hold them.
func noteRows(home, shared *model.ClassCapability, mode model.HomeMode) []Row {
	var rows []Row
	for _, c := range []*model.ClassCapability{home, shared} {
		if c == nil || (c == shared && home != nil && home.Name == shared.Name) {
			continue
		}
		for _, note := range c.Notes {
			if note == "NeedsNfsClient" && c != shared && mode != model.HomeModeShared {
				continue
			}
			r := notice("note:"+note, string(c.RWXFile.Provenance), reasons.NoticeCode(note), c.Name)
			r.Class = c.Name
			rows = append(rows, r)
		}
	}
	return rows
}

// maxMountEvidence caps the homes a mount row quotes.
const maxMountEvidence = 3

// mountRow reports home folders the machines failed to mount: the homes that
// failed so, and a storage test of the home class that failed at its mount
// step. When the evidence is the mount helper missing on shared homes, the
// row says the machines have no NFS client and how to avoid it.
func mountRow(cs *model.ClusterStorage, homes []model.Home) (Row, bool) {
	ch := cs.Status.Choice
	var evidence []string
	for _, h := range homes {
		if c := meta.FindStatusCondition(h.Status.Conditions, model.ConditionReady); c != nil && c.Reason == string(reasons.HomeMountFailed) {
			evidence = append(evidence, fmt.Sprintf("home of %s: %s", h.Spec.Owner, c.Message))
		}
	}
	if len(evidence) > maxMountEvidence {
		evidence = append(evidence[:maxMountEvidence], fmt.Sprintf("and %d more", len(evidence)-maxMountEvidence))
	}
	for _, p := range cs.Status.ProbeResults {
		if p.StorageClassName == ch.HomeClass && p.Result == model.ProbeFailed && p.FailedStep == "mount" {
			evidence = append(evidence, p.Evidence...)
		}
	}
	if len(evidence) == 0 {
		return Row{}, false
	}
	code := reasons.AdminHomesCannotMount
	if ch.Mode == model.HomeModeShared && slices.ContainsFunc(evidence, missingMountHelper) {
		code = reasons.AdminSharedHomesNoNfsClient
	}
	r := notice("mount", "", code, ch.HomeClass)
	r.Class, r.Evidence = ch.HomeClass, evidence
	return r, true
}

// missingMountHelper reports whether a mount failed because the machine lacks
// the helper for the filesystem: mount(8) then says "you might need a
// /sbin/mount.<type> helper program", which for NFS means no NFS client.
func missingMountHelper(line string) bool {
	return strings.Contains(line, "helper program")
}

func probeRow(p model.ProbeStatus) Row {
	if p.Result == model.ProbeFailed {
		r := notice("probe", string(model.ProvenanceVerified), reasons.AdminProbeFailed, p.StorageClassName, p.FailedStep)
		r.Evidence = slices.Clone(p.Evidence)
		return r
	}
	return notice("probe", string(model.ProvenanceVerified), reasons.AdminProbePassed, p.StorageClassName, p.NodesTested)
}

func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Text writes rows for a terminal: the title with its provenance in brackets, then the text, the action and the
// evidence, indented. Shared storage nobody has tested says so in the brackets.
func Text(w io.Writer, rows []Row) error {
	for _, r := range rows {
		line := r.Notice.Title
		if r.Provenance != "" {
			prov := r.Provenance
			if r.Notice.Code == reasons.AdminSharedFoundNotVerified {
				prov += ", not verified"
			}
			line += "   [" + prov + "]"
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
		for _, l := range []string{r.Notice.Text, r.Notice.Action, r.Notice.Detail} {
			if l == "" {
				continue
			}
			if _, err := fmt.Fprintf(w, "  %s\n", l); err != nil {
				return err
			}
		}
		for _, e := range r.Evidence {
			if _, err := fmt.Fprintf(w, "  %s\n", e); err != nil {
				return err
			}
		}
	}
	return nil
}
