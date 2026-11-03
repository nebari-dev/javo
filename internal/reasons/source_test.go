package reasons

import (
	"strings"
	"testing"
)

// The codes the repository, build and preview features emit, with whose fault
// each one is.
var sourceCodes = map[Code]Party{
	RepoNotFound:        PartyUser,
	RefNotFound:         PartyUser,
	AuthFailed:          PartyUser,
	CloneFailed:         PartyPlatform,
	FileNotInRepo:       PartyUser,
	BuildFailed:         PartyUser,
	BuildOutputMissing:  PartyUser,
	PreviewRateLimited:  PartyPlatform,
	PreviewLimitReached: PartyUser,
	PreviewForkSkipped:  PartyUser,
	PreviewsUnavailable: PartyPlatform,
}

func TestSourceCodesHaveCopyAndAgreeingBlame(t *testing.T) {
	opening := map[Party]string{PartyUser: "Your", PartyPlatform: "The platform", PartyTemplate: "The template"}
	for code, party := range sourceCodes {
		r, ok := Lookup(code)
		if !ok {
			t.Errorf("%s missing from catalog", code)
			continue
		}
		if r.Title == "" || r.What == "" || r.Todo == "" || r.Who == "" || r.Blame == "" {
			t.Errorf("%s has empty copy: %+v", code, r)
		}
		if r.Blamed != party {
			t.Errorf("%s blamed %s, want %s", code, r.Blamed, party)
		}
		if !strings.HasPrefix(r.Blame, opening[r.Blamed]) {
			t.Errorf("%s blamed %s but Blame opens %q", code, r.Blamed, r.Blame)
		}
	}
}

func TestRepoNotFoundNamesBothCauses(t *testing.T) {
	r, _ := Lookup(RepoNotFound)
	if r.What != "The repository was not found. It may be misspelled, or private and need a token." {
		t.Errorf("What = %q", r.What)
	}
}

func TestBuildOutputMissingNamesTheFolder(t *testing.T) {
	r, _ := Lookup(BuildOutputMissing)
	if !strings.Contains(r.What+r.Todo, "output folder") {
		t.Errorf("copy does not point at the output folder: %+v", r)
	}
}

func TestSourceMessagesAreInCatalogAndTakeTheirArguments(t *testing.T) {
	plain := []MessageCode{
		MsgGitURLInvalid, MsgGitURLNotHTTPS, MsgGitURLHasCredentials, MsgSourcePathInvalid,
		MsgPreviewsNeedGitHub, MsgTemplateNeedsRepo, MsgTemplateNoRepo,
		MsgWebhookSecretOnce, MsgPreviewManaged, MsgNothingToRedeploy,
		MsgWebhookSignature, MsgWebhookTooLarge, MsgWebhookRateLimited, MsgWebhookUnknown,
	}
	for _, c := range plain {
		if Msg(c) == Msg(MsgInvalidRequest) {
			t.Errorf("%s is not in the catalog", c)
		}
		if strings.Contains(Msg(c), "%") {
			t.Errorf("%s takes no argument but has a verb: %q", c, Msg(c))
		}
	}
	if got := Msg(MsgPreviewsTooMany, 10); !strings.Contains(got, "10") || strings.Contains(got, "%!") {
		t.Errorf("MsgPreviewsTooMany = %q", got)
	}
}
