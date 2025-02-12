package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/evergreen/thirdparty"
	"github.com/evergreen-ci/evergreen/units"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/evergreen-ci/gimlet"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
	mgobson "gopkg.in/mgo.v2/bson"
)

const formMimeType = "application/x-www-form-urlencoded"

var cliOutOfDateError = errors.New("CLI is out of date: use 'evergreen get-update --install'")

// PatchAPIResponse is returned by all patch-related API calls
type PatchAPIResponse struct {
	Message string       `json:"message"`
	Action  string       `json:"action"`
	Patch   *patch.Patch `json:"patch"`
}

// submitPatch creates the Patch document, adds the patched project config to it,
// and saves the patches to GridFS to be retrieved
func (as *APIServer) submitPatch(w http.ResponseWriter, r *http.Request) {
	dbUser := MustHaveUser(r)

	data := struct {
		Description       string             `json:"desc"`
		Path              string             `json:"path"`
		Project           string             `json:"project"`
		BackportInfo      patch.BackportInfo `json:"backport_info"`
		GitMetadata       *patch.GitMetadata `json:"git_metadata"`
		PatchBytes        []byte             `json:"patch_bytes"`
		Githash           string             `json:"githash"`
		Parameters        []patch.Parameter  `json:"parameters"`
		Variants          []string           `json:"buildvariants_new"`
		Tasks             []string           `json:"tasks"`
		SyncBuildVariants []string           `json:"sync_build_variants"`
		SyncTasks         []string           `json:"sync_tasks"`
		SyncStatuses      []string           `json:"sync_statuses"`
		SyncTimeout       time.Duration      `json:"sync_timeout"`
		Finalize          bool               `json:"finalize"`
		TriggerAliases    []string           `json:"trigger_aliases"`
		Alias             string             `json:"alias"`
		ReuseDefinition   bool               `json:"reuse_definition"`
	}{}
	if err := utility.ReadJSON(util.NewRequestReaderWithSize(r, patch.SizeLimit), &data); err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, err)
		return
	}
	pref, err := model.FindMergedProjectRef(data.Project)
	if err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, errors.Wrapf(err, "project '%s' is not specified", data.Project))
		return
	}
	if pref == nil {
		gimlet.WriteJSONResponse(w, http.StatusNotFound,
			gimlet.ErrorResponse{
				StatusCode: http.StatusNotFound,
				Message:    fmt.Sprintf("project '%s' is not found", data.Project),
			})
		return
	}

	opts := gimlet.PermissionOpts{
		Resource:      pref.Id,
		ResourceType:  evergreen.ProjectResourceType,
		Permission:    evergreen.PermissionPatches,
		RequiredLevel: evergreen.PatchSubmit.Value,
	}
	if !dbUser.HasPermission(opts) {
		as.LoggedError(w, r, http.StatusUnauthorized, errors.New("user is not authorized to patch this project"))
		return
	}

	patchString := string(data.PatchBytes)
	if len(patchString) > patch.SizeLimit {
		as.LoggedError(w, r, http.StatusBadRequest, errors.New("Patch is too large"))
		return
	}

	if data.Alias == evergreen.CommitQueueAlias && len(patchString) != 0 && !patch.IsMailboxDiff(patchString) {
		as.LoggedError(w, r, http.StatusBadRequest, cliOutOfDateError)
		return
	}

	if pref.IsPatchingDisabled() || !pref.IsEnabled() {
		as.LoggedError(w, r, http.StatusUnauthorized, errors.New("patching is disabled"))
		return
	}

	if !pref.TaskSync.IsPatchEnabled() && (len(data.SyncTasks) != 0 || len(data.SyncBuildVariants) != 0) {
		as.LoggedError(w, r, http.StatusUnauthorized, errors.New("task sync at the end of a patched task is disabled by project settings"))
		return
	}

	intent, err := patch.NewCliIntent(patch.CLIIntentParams{
		User:            dbUser.Id,
		Project:         pref.Id,
		Path:            data.Path,
		BaseGitHash:     data.Githash,
		Module:          r.FormValue("module"),
		PatchContent:    patchString,
		Description:     data.Description,
		Finalize:        data.Finalize,
		Parameters:      data.Parameters,
		Variants:        data.Variants,
		Tasks:           data.Tasks,
		Alias:           data.Alias,
		TriggerAliases:  data.TriggerAliases,
		BackportOf:      data.BackportInfo,
		GitInfo:         data.GitMetadata,
		ReuseDefinition: data.ReuseDefinition,
		SyncParams: patch.SyncAtEndOptions{
			BuildVariants: data.SyncBuildVariants,
			Tasks:         data.SyncTasks,
			Statuses:      data.SyncStatuses,
			Timeout:       data.SyncTimeout,
		},
	})

	if err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, err)
		return
	}

	if intent == nil {
		as.LoggedError(w, r, http.StatusBadRequest, errors.New("intent could not be created from supplied data"))
		return
	}
	if err = intent.Insert(); err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, err)
		return
	}

	patchID := mgobson.NewObjectId()
	grip.Info(message.Fields{
		"operation":  "patch creation",
		"message":    "creating patch",
		"from":       "CLI",
		"patch_id":   patchID,
		"finalizing": data.Finalize,
		"variants":   data.Variants,
		"tasks":      data.Tasks,
		"alias":      data.Alias,
	})
	job := units.NewPatchIntentProcessor(patchID, intent)
	job.Run(r.Context())

	if err = job.Error(); err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, errors.Wrap(err, "error processing patch"))
		return
	}

	patchDoc, err := patch.FindOne(patch.ById(patchID))
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, errors.New("can't fetch patch data"))
		return
	}
	if patchDoc == nil {
		as.LoggedError(w, r, http.StatusInternalServerError, errors.New("patch couldn't be found"))
		return
	}

	gimlet.WriteJSONResponse(w, http.StatusCreated, PatchAPIResponse{Patch: patchDoc})
}

// Get the patch with the specified request it
func getPatchFromRequest(r *http.Request) (*patch.Patch, error) {
	// get id and secret from the request.
	patchIdStr := gimlet.GetVars(r)["patchId"]
	if len(patchIdStr) == 0 {
		return nil, errors.New("no patch id supplied")
	}

	// find the patch
	existingPatch, err := patch.FindOneId(patchIdStr)
	if err != nil {
		return nil, err
	}
	if existingPatch == nil {
		return nil, errors.Errorf("no existing request with id: %v", patchIdStr)
	}
	return existingPatch, nil
}

func (as *APIServer) updatePatchModule(w http.ResponseWriter, r *http.Request) {
	p, err := getPatchFromRequest(r)
	if err != nil {
		gimlet.WriteJSONError(w, err.Error())
		return
	}

	if p.Version != "" && p.IsCommitQueuePatch() {
		as.LoggedError(w, r, http.StatusBadRequest, errors.New("can't update modules for in-flight commit queue tests"))
		return
	}

	githubOauthToken, err := as.Settings.GetGithubOauthToken()
	if err != nil {
		gimlet.WriteJSONError(w, err)
		return
	}

	data := struct {
		Module     string `json:"module"`
		PatchBytes []byte `json:"patch_bytes"`
		Githash    string `json:"githash"`
	}{}
	if err = utility.ReadJSON(util.NewRequestReader(r), &data); err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, err)
		return
	}

	patchContent := string(data.PatchBytes)
	if p.IsCommitQueuePatch() && len(patchContent) != 0 && !patch.IsMailboxDiff(patchContent) {
		as.LoggedError(w, r, http.StatusBadRequest, errors.New("You may be using 'set-module' instead of 'commit-queue set-module', or your CLI may be out of date.\n"+
			"Please update your CLI if it is not up to date, and use 'commit-queue set-module' instead of 'set-module' for commit queue patches."))
		return
	}

	moduleName, githash := data.Module, data.Githash

	projectRef, err := model.FindOneProjectRef(p.Project)
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, errors.Wrapf(err, "Error getting project ref with id %v", p.Project))
		return
	}
	_, project, err := model.FindLatestVersionWithValidProject(projectRef.Id)
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, errors.Wrap(err, "Error getting patch"))
		return
	}
	if project == nil {
		as.LoggedError(w, r, http.StatusNotFound, errors.Errorf("can't find project: %v", p.Project))
		return
	}

	module, err := project.GetModuleByName(moduleName)
	if err != nil || module == nil {
		as.LoggedError(w, r, http.StatusBadRequest, errors.Errorf("No such module: %s", moduleName))
		return
	}

	var summaries []thirdparty.Summary
	var commitMessages []string
	if patch.IsMailboxDiff(patchContent) {
		summaries, commitMessages, err = thirdparty.GetPatchSummariesFromMboxPatch(patchContent)
		if err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, errors.Errorf("Error getting summaries by commit"))
			return
		}
	} else {
		summaries, err = thirdparty.GetPatchSummaries(patchContent)
		if err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, err)
			return
		}
	}

	repoOwner, repo := module.GetRepoOwnerAndName()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	_, err = thirdparty.GetCommitEvent(ctx, githubOauthToken, repoOwner, repo, githash)
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, err)
		return
	}

	// write the patch content into a GridFS file under a new ObjectId.
	patchFileId := mgobson.NewObjectId().Hex()
	err = db.WriteGridFile(patch.GridFSPrefix, patchFileId, strings.NewReader(patchContent))
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, errors.Wrap(err, "failed to write patch file to db"))
		return
	}

	modulePatch := patch.ModulePatch{
		ModuleName: moduleName,
		Githash:    githash,
		IsMbox:     len(patchContent) == 0 || patch.IsMailboxDiff(patchContent),
		PatchSet: patch.PatchSet{
			PatchFileId:    patchFileId,
			Summary:        summaries,
			CommitMessages: commitMessages,
		},
	}
	if err = p.UpdateModulePatch(modulePatch); err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, err)
		return
	}

	if p.IsCommitQueuePatch() {
		if err = p.SetDescription(model.MakeCommitQueueDescription(p.Patches, projectRef, project)); err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, err)
			return
		}
	}

	gimlet.WriteJSON(w, "Patch module updated")
}

// listPatches returns a user's "n" most recent patches.
func (as *APIServer) listPatches(w http.ResponseWriter, r *http.Request) {
	dbUser := MustHaveUser(r)
	n, err := util.GetIntValue(r, "n", 0)
	if err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, errors.Wrap(err, "cannot read value n"))
		return
	}
	filterCommitQueue := r.FormValue("filter_commit_queue") == "true"
	query := patch.ByUserAndCommitQueue(dbUser.Id, filterCommitQueue).Sort([]string{"-" + patch.CreateTimeKey})
	if n > 0 {
		query = query.Limit(n)
	}
	patches, err := patch.Find(query)
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError,
			errors.Wrapf(err, "error finding patches for user %s", dbUser.Id))
		return
	}
	gimlet.WriteJSON(w, patches)
}

func (as *APIServer) existingPatchRequest(w http.ResponseWriter, r *http.Request) {
	dbUser := MustHaveUser(r)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	p, err := getPatchFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	var action, desc string
	if r.Header.Get("Content-Type") == formMimeType {
		action = r.FormValue("action")
	} else {
		data := struct {
			PatchId     string `json:"patch_id"`
			Action      string `json:"action"`
			Description string `json:"description"`
		}{}
		if err = utility.ReadJSON(util.NewRequestReader(r), &data); err != nil {
			as.LoggedError(w, r, http.StatusBadRequest, err)
			return
		}
		action, desc = data.Action, data.Description
	}

	if p.IsCommitQueuePatch() {
		as.LoggedError(w, r, http.StatusBadRequest, errors.New("can't modify a commit queue patch"))
		return
	}
	// dispatch to handlers based on specified action
	switch action {
	case "update":
		err = p.SetDescription(desc)
		if err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, err)
			return
		}
		gimlet.WriteJSON(w, "patch updated")
	case "finalize":
		var githubOauthToken string
		githubOauthToken, err = as.Settings.GetGithubOauthToken()
		if err != nil {
			gimlet.WriteJSONInternalError(w, err)
			return
		}

		if p.Activated {
			http.Error(w, "patch is already finalized", http.StatusBadRequest)
			return
		}

		var projectYaml string
		_, projectYaml, err = model.GetPatchedProject(ctx, p, githubOauthToken)
		if err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, err)
			return
		}

		p.PatchedConfig = projectYaml
		_, err = model.FinalizePatch(ctx, p, evergreen.PatchVersionRequester, githubOauthToken)
		if err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, err)
			return
		}
		grip.Info(message.Fields{
			"operation":     "patch creation",
			"message":       "finalized patch",
			"from":          "CLI",
			"patch_id":      p.Id,
			"variants":      p.BuildVariants,
			"tasks":         p.Tasks,
			"variant_tasks": p.VariantsTasks,
			"alias":         p.Alias,
		})

		gimlet.WriteJSON(w, "patch finalized")
	case "cancel":
		err = model.CancelPatch(p, task.AbortInfo{User: dbUser.Id})
		if err != nil {
			as.LoggedError(w, r, http.StatusInternalServerError, err)
			return
		}
		gimlet.WriteJSON(w, "patch deleted")
	default:
		http.Error(w, fmt.Sprintf("Unrecognized action: %v", action), http.StatusBadRequest)
	}
}

func (as *APIServer) summarizePatch(w http.ResponseWriter, r *http.Request) {
	p, err := getPatchFromRequest(r)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	gimlet.WriteJSON(w, PatchAPIResponse{Patch: p})
}

func (as *APIServer) listPatchModules(w http.ResponseWriter, r *http.Request) {
	_, project := MustHaveProject(r)

	p, err := getPatchFromRequest(r)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	data := struct {
		Project string   `json:"project"`
		Modules []string `json:"modules"`
	}{
		Project: project.Identifier,
	}

	mods := map[string]struct{}{}

	for _, m := range project.Modules {
		if m.Name == "" {
			continue
		}
		mods[m.Name] = struct{}{}
	}

	for _, m := range p.Patches {
		mods[m.ModuleName] = struct{}{}
	}

	for m := range mods {
		data.Modules = append(data.Modules, m)
	}
	gimlet.WriteJSON(w, &data)
}

func (as *APIServer) deletePatchModule(w http.ResponseWriter, r *http.Request) {
	p, err := getPatchFromRequest(r)
	if err != nil {
		as.LoggedError(w, r, http.StatusBadRequest, err)
		return
	}
	moduleName := r.FormValue("module")
	if moduleName == "" {
		gimlet.WriteJSONError(w, "You must specify a module to delete")
		return
	}

	// don't mess with already finalized requests
	if p.Activated {
		response := fmt.Sprintf("Can't delete module - path already finalized")
		gimlet.WriteJSONError(w, response)
		return
	}

	err = p.RemoveModulePatch(moduleName)
	if err != nil {
		as.LoggedError(w, r, http.StatusInternalServerError, err)
		return
	}

	gimlet.WriteJSON(w, PatchAPIResponse{Message: "module removed from patch."})
}
