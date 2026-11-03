package reasons

import (
	"strings"
	"testing"
)

func TestMessagesHaveTextWithoutDashes(t *testing.T) {
	if len(AllMessages()) == 0 {
		t.Fatal("no messages")
	}
	for code, text := range AllMessages() {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s has no text", code)
		}
		if strings.ContainsAny(text, "\u2014\u2013") {
			t.Errorf("%s contains an em or en dash: %q", code, text)
		}
	}
}

func TestMsgFormatsArguments(t *testing.T) {
	if got := Msg(MsgNameTakenByYou, "Sales app"); got != `You already have an app called "Sales app".` {
		t.Fatalf("got %q", got)
	}
}

func TestMsgUnknownCodeFallsBackToGeneric(t *testing.T) {
	if got := Msg(MessageCode("Nope")); got != Msg(MsgInvalidRequest) {
		t.Fatalf("got %q", got)
	}
}

func TestSampleMessages(t *testing.T) {
	if got := Msg(MsgNoSample, "Plain"); !strings.Contains(got, "Plain") {
		t.Fatalf("MsgNoSample does not name the template: %q", got)
	}
	if Msg(MsgSampleNameTaken) == Msg(MsgInvalidRequest) {
		t.Fatal("MsgSampleNameTaken is not in the catalog")
	}
}

func TestJobMessagesAreInCatalogAndCarryArguments(t *testing.T) {
	plain := []MessageCode{MsgJobNotFound, MsgRunNotFound, MsgTimeZoneRequired, MsgJobsNotOnTemplate,
		MsgAppLoginNeedsNetworkPolicy, MsgResumeStartsMissedRun, MsgOutputTooLarge, MsgRunStillActive, MsgFileNotFound}
	for _, c := range plain {
		if Msg(c) == Msg(MsgInvalidRequest) {
			t.Errorf("%s is not in the catalog", c)
		}
	}
	withArgs := []struct {
		code MessageCode
		args []any
	}{
		{MsgCronInvalid, []any{"hour must be 0 to 23"}},
		{MsgTimeZoneInvalid, []any{"Mars/Base"}},
		{MsgEngineNotInTemplate, []any{"python", "quarto"}},
		{MsgKernelNotInTemplate, []any{"python", "julia-1.11"}},
		{MsgTimeoutTooLong, []any{"2h"}},
		{MsgParamInvalid, []any{"region", "string"}},
		{MsgFileWrongEngine, []any{".ipynb, .py"}},
		{MsgDSTSkipped, []any{"Sun 28 Mar 2027", "the 01:30 run"}},
		{MsgDSTRepeated, []any{"Sun 31 Oct 2027", "the runs from 01:15 to 01:45"}},
		{MsgDSTOneRun, []any{"01:30"}},
		{MsgDSTRuns, []any{"01:15", "01:45"}},
		{MsgLogsGone, nil},
	}
	for _, c := range withArgs {
		got := Msg(c.code, c.args...)
		if strings.Contains(got, "%!") {
			t.Errorf("%s text %q does not take %d arguments", c.code, got, len(c.args))
		}
		if got == Msg(MsgInvalidRequest) {
			t.Errorf("%s is not in the catalog", c.code)
		}
		for _, a := range c.args {
			if !strings.Contains(got, a.(string)) {
				t.Errorf("%s text %q lacks argument %q", c.code, got, a)
			}
		}
	}
}

func TestTemplateManagedByHelmIsInCatalog(t *testing.T) {
	if Msg(MsgTemplateManagedByHelm) == Msg(MsgInvalidRequest) {
		t.Fatal("MsgTemplateManagedByHelm is not in the catalog")
	}
}

func TestMinutesInWords(t *testing.T) {
	for m, want := range map[int32]string{1: "1 minute", 45: "45 minutes", 60: "60 minutes", 90: "90 minutes",
		120: "2 hours", 1440: "1 day", 2880: "2 days", 10080: "7 days"} {
		if got := Minutes(m); got != want {
			t.Errorf("Minutes(%d) = %q, want %q", m, got, want)
		}
	}
}

func TestIdleStopMessages(t *testing.T) {
	if got := Msg(MsgIdleStopOutOfRange, Minutes(10), Minutes(10080)); got != "Pick between 10 minutes and 7 days." {
		t.Fatalf("got %q", got)
	}
	if got := Msg(MsgStoppedIdle, Minutes(60)); got != "Stopped after 60 minutes without use" {
		t.Fatalf("got %q", got)
	}
	if Msg(MsgIdleStopOffNotAllowed) == Msg(MsgInvalidRequest) {
		t.Fatal("MsgIdleStopOffNotAllowed is not in the catalog")
	}
}
