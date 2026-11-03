package preview

import (
	"fmt"

	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

// Child is the app that previews pr: the parent's own settings, run from the
// pull request's head commit. It has no previews of its own and no API
// access, whatever the template defaults to, because it runs code the owner
// has not necessarily written. A fork's child is marked so its pod gets no
// repository token, and opens for the owner only: the parent's audience did
// not agree to run a stranger's code.
func Child(parent *model.App, pr codehost.PullRequest) model.App {
	name := parent.DisplayName
	if name == "" {
		name = parent.Name
	}
	src := *parent.Source
	src.Ref, src.Previews, src.Fork = pr.HeadSHA, nil, pr.Fork
	if parent.Source.Build != nil {
		build := *parent.Source.Build
		src.Build = &build
	}
	child := model.App{
		Namespace:   parent.Namespace,
		Name:        naming.PreviewName(parent.Name, pr.Number),
		Owner:       parent.Owner,
		DisplayName: fmt.Sprintf("%s PR #%d", name, pr.Number),
		Template:    parent.Template,
		File:        parent.File,
		Size:        parent.Size,
		Command:     parent.Command,
		Image:       parent.Image,
		Port:        parent.Port,
		APIAccess:   model.APIAccessNone,
		Source:      &src,
		Environment: parent.Environment,
		Sharing:     *parent.Sharing.DeepCopy(),
		ParentID:    parent.ID,
		PullNumber:  pr.Number,
	}
	if pr.Fork {
		child.Sharing = model.Sharing{Access: model.AccessPrivate}
	}
	if parent.Resources != nil {
		child.Resources = &model.AppResources{CPU: parent.Resources.CPU.DeepCopy(), Memory: parent.Resources.Memory.DeepCopy()}
	}
	return child
}
